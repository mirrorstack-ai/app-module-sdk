package contributions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/mirrorstack-ai/app-module-sdk/db"
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

// EnsureTable runs CREATE TABLE IF NOT EXISTS. Dev fallback; prod
// schema management lives in the lifecycle install hook.
func (s *Storage) EnsureTable(ctx context.Context, q db.Querier) error {
	// Index name strips the surrounding quotes from the sanitized
	// table name so it stays a bare identifier (Postgres errors on
	// quoted index names that contain double-quotes).
	idx := s.tableUnquoted() + "_slot_registered_idx"
	stmt := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %[1]s (
		    slot            text NOT NULL,
		    contribution_id text NOT NULL,
		    payload         jsonb NOT NULL,
		    registered_at   timestamptz NOT NULL DEFAULT now(),
		    PRIMARY KEY (slot, contribution_id)
		);

		CREATE INDEX IF NOT EXISTS %[2]s
		    ON %[1]s (slot, registered_at DESC);`,
		s.table, idx,
	)
	_, err := q.Exec(ctx, stmt)
	return err
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
		if err := rows.Scan(&c.ID, &c.Payload, &c.RegisteredAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
