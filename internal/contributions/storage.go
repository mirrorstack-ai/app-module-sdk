package contributions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/pgerr"
)

// listLimit caps the row count returned by Storage.List so a slot
// with thousands of contributions (unlikely today) can't return a
// pathologically large response. Picked to comfortably cover every
// realistic slot count in v1.
const listLimit = 1000

// Storage wraps the contributions table CRUD. Sanitized name is
// computed once at construction so the SQL builders don't pay the
// identifier-escape cost per call.
type Storage struct {
	table string // already pgx-Sanitize'd, safe to interpolate
}

// NewStorage constructs a Storage rooted at the given module ID.
// modulePrefix is validated upstream against moduleIDPattern in
// core.New(), so Sanitize is belt-and-suspenders.
func NewStorage(modulePrefix string) *Storage {
	return &Storage{table: pgx.Identifier{modulePrefix + "_contributions"}.Sanitize()}
}

// TableName returns the SQL-safe (already sanitized + quoted)
// contributions table name. Exposed for test diagnostics.
func (s *Storage) TableName() string { return s.table }

// EnsureTable creates the store in whatever schema q's search_path resolves to.
// The store is per-APP (one table per module per app schema), so every caller
// must already be scoped to one app — there is no once-per-process shortcut: a
// deployed module's container serves many apps.
//
// Callers: the lifecycle install/upgrade hook (the platform's per-(app, module)
// window, which is what provisions a deployed install), the per-app lazy dev
// provisioner, and WithTable's retry (which heals an install that predates the
// hook). All are idempotent.
//
// CONCURRENCY. CREATE TABLE IF NOT EXISTS is idempotent but NOT atomic — the
// existence check and the catalog insert race, so two cold containers
// provisioning the same app at the same instant can both pass the check and
// then collide in pg_class. Two things prevent that, deliberately both:
//
//   - The advisory lock serializes provisioning per SCHEMA. Keyed on the
//     resolved schema rather than on the module because the store is per-app:
//     two apps must not queue behind each other, two cold starts for the SAME
//     app must. It is the xact-scoped variant so it is released by the implicit
//     transaction that ends this Exec, never leaked onto a pooled connection.
//   - pgerr.RelationAlreadyExists is the backstop for what the lock cannot
//     cover — a psql session, or any future non-SDK writer — at the cost of one
//     error-code comparison.
//
// The three statements MUST stay in one argument-less Exec: pgx sends that over
// the simple protocol, which Postgres runs as a single implicit transaction, and
// that is the only thing making pg_advisory_xact_lock outlive its own statement
// and still cover the CREATEs.
func (s *Storage) EnsureTable(ctx context.Context, q db.Querier) error {
	// Index name strips the surrounding quotes from the sanitized
	// table name so it stays a bare identifier (Postgres errors on
	// quoted index names that contain double-quotes).
	bare := s.tableUnquoted()
	idx := bare + "_slot_registered_idx"
	// bare is <modulePrefix>_contributions, and modulePrefix is validated
	// against moduleIDPattern in core.New(), so it cannot close the lock key's
	// string literal.
	stmt := fmt.Sprintf(`
		SELECT pg_advisory_xact_lock(hashtext(current_schema() || '.%[3]s'));

		CREATE TABLE IF NOT EXISTS %[1]s (
		    slot            text NOT NULL,
		    contribution_id text NOT NULL,
		    payload         jsonb NOT NULL,
		    registered_at   timestamptz NOT NULL DEFAULT now(),
		    PRIMARY KEY (slot, contribution_id)
		);

		CREATE INDEX IF NOT EXISTS %[2]s
		    ON %[1]s (slot, registered_at DESC);`,
		s.table, idx, bare,
	)
	if _, err := q.Exec(ctx, stmt); err != nil && !pgerr.RelationAlreadyExists(err) {
		return err
	}
	return nil
}

// WithTable runs op and, when op failed because the store does not exist in this
// app's schema, creates it and runs op once more.
//
// This is the heal path for an install that predates the lifecycle provisioning
// hook: the platform only opens an install/upgrade window when a migration
// watermark moves, so an app already installed at the current version gets no
// further lifecycle call and would otherwise never acquire the table. 42P01 is
// the only error it acts on — the one code that means "never provisioned for
// this app" rather than "this query is wrong" — so a provisioned store pays
// nothing and a permission failure still surfaces to the caller.
//
// q MUST NOT be inside a caller-owned transaction: the failed statement aborts
// it, so neither the CREATE nor the retry could run. Both call sites (the
// /contrib handlers and Module.StoredContributions) hold a plain pooled
// connection for exactly that reason.
func (s *Storage) WithTable(ctx context.Context, q db.Querier, op func() error) error {
	err := op()
	if !pgerr.UndefinedTable(err) {
		return err
	}
	if ensureErr := s.EnsureTable(ctx, q); ensureErr != nil {
		return fmt.Errorf("contributions: provision %s: %w", s.table, ensureErr)
	}
	return op()
}

// tableUnquoted returns the table name without the pgx-Sanitize
// surrounding double quotes — needed when building an index name
// (which must itself be an identifier, not a quoted-string).
func (s *Storage) tableUnquoted() string {
	t := s.table
	if len(t) >= 2 && t[0] == '"' && t[len(t)-1] == '"' {
		return t[1 : len(t)-1]
	}
	return t
}

// Upsert writes a contribution. Idempotent on (slot, contribution_id).
func (s *Storage) Upsert(ctx context.Context, q db.Querier, slot, id string, payload json.RawMessage) error {
	if id == "" {
		return ErrEmptyID
	}
	if !json.Valid(payload) {
		return errors.Join(ErrInvalidPayload, errors.New("storage: payload is not valid JSON"))
	}
	stmt := fmt.Sprintf(`
		INSERT INTO %s (slot, contribution_id, payload, registered_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (slot, contribution_id)
		DO UPDATE SET payload = EXCLUDED.payload, registered_at = now()`,
		s.table,
	)
	_, err := q.Exec(ctx, stmt, slot, id, payload)
	return err
}

// Delete removes a contribution. Returns pgx.ErrNoRows when the row
// didn't exist; the HTTP layer maps that to 404 (double-delete is
// not silently idempotent here).
func (s *Storage) Delete(ctx context.Context, q db.Querier, slot, id string) error {
	stmt := fmt.Sprintf(`DELETE FROM %s WHERE slot = $1 AND contribution_id = $2`, s.table)
	tag, err := q.Exec(ctx, stmt, slot, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// List returns up to listLimit contributions registered against
// `slot`, newest first.
func (s *Storage) List(ctx context.Context, q db.Querier, slot string) ([]Contribution, error) {
	stmt := fmt.Sprintf(`
		SELECT contribution_id, payload, registered_at
		FROM %s
		WHERE slot = $1
		ORDER BY registered_at DESC
		LIMIT %d`,
		s.table, listLimit,
	)
	rows, err := q.Query(ctx, stmt, slot)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Contribution{}
	for rows.Next() {
		var c Contribution
		if err := rows.Scan(&c.OwnerModuleID, &c.Payload, &c.RegisteredAt); err != nil {
			return nil, err
		}
		c.ID = c.OwnerModuleID
		out = append(out, c)
	}
	return out, rows.Err()
}
