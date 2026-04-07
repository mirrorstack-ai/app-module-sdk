package migration

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"

	"github.com/mirrorstack-ai/app-module-sdk/db"
)

// Migration represents one migration on disk: an up-file with a numeric
// version prefix, an optional matching down-file, and the human-readable slug.
type Migration struct {
	Version  string // numeric prefix from filename, e.g., "0008"
	Name     string // human-readable slug, e.g., "add_index"
	UpFile   string // path within fsys, e.g., "sql/0008_add_index.up.sql"
	DownFile string // path within fsys, "" if no down file present
}

// trackingTableSQL is the schema-migrations table created in the active app
// schema (the connection's search_path is set by db.AcquireScoped). The
// double-underscore prefix matches the /__mirrorstack/* route convention and
// avoids collisions with module tables.
const trackingTableSQL = `CREATE TABLE IF NOT EXISTS __mirrorstack_migrations (
	version    TEXT PRIMARY KEY,
	name       TEXT NOT NULL,
	applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`

// List returns all migrations from sql/, sorted by numeric version (ascending).
// A migration is identified by its .up.sql file; the matching .down.sql is
// recorded if present (empty otherwise — downgrades will fail for that version).
//
// Returns an empty slice (not an error) if fsys is nil or sql/ does not exist.
func List(fsys fs.FS) ([]Migration, error) {
	if fsys == nil {
		return nil, nil
	}
	entries, err := fs.ReadDir(fsys, "sql")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("mirrorstack/migration: read sql/ dir: %w", err)
	}

	// First pass: collect filenames into two maps keyed by version.
	upFiles := make(map[string]string)   // version → full path
	downFiles := make(map[string]string) // version → full path
	names := make(map[string]string)     // version → slug (from up file)

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if version, slug, ok := parseUpFilename(e.Name()); ok {
			upFiles[version] = "sql/" + e.Name()
			names[version] = slug
			continue
		}
		if m := downFilePattern.FindStringSubmatch(e.Name()); m != nil {
			downFiles[m[1]] = "sql/" + e.Name()
		}
	}

	// Second pass: build Migration records sorted numerically by version.
	versions := make([]string, 0, len(upFiles))
	for v := range upFiles {
		versions = append(versions, v)
	}
	sort.Slice(versions, func(i, j int) bool {
		ni, _ := strconv.Atoi(versions[i])
		nj, _ := strconv.Atoi(versions[j])
		return ni < nj
	})

	out := make([]Migration, 0, len(versions))
	for _, v := range versions {
		out = append(out, Migration{
			Version:  v,
			Name:     names[v],
			UpFile:   upFiles[v],
			DownFile: downFiles[v], // "" if no matching down file
		})
	}
	return out, nil
}

// EnsureTrackingTable creates __mirrorstack_migrations if it doesn't already
// exist. Idempotent. Called automatically by Apply / ApplyDown — exposed for
// callers that want to verify the table is present without applying anything.
func EnsureTrackingTable(ctx context.Context, q db.Querier) error {
	if _, err := q.Exec(ctx, trackingTableSQL); err != nil {
		return fmt.Errorf("mirrorstack/migration: ensure tracking table: %w", err)
	}
	return nil
}

// AppliedVersions returns the set of versions currently recorded in the
// tracking table. Used by Apply to skip already-applied migrations.
func AppliedVersions(ctx context.Context, q db.Querier) (map[string]bool, error) {
	rows, err := q.Query(ctx, "SELECT version FROM __mirrorstack_migrations")
	if err != nil {
		return nil, fmt.Errorf("mirrorstack/migration: read applied versions: %w", err)
	}
	defer rows.Close()

	out := make(map[string]bool)
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("mirrorstack/migration: scan applied version: %w", err)
		}
		out[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mirrorstack/migration: iterate applied versions: %w", err)
	}
	return out, nil
}

// TxRunner runs fn inside a database transaction. Matches Module.Tx signature
// so handlers can pass it directly without leaking pgxpool out of the system
// package.
type TxRunner func(ctx context.Context, fn func(q db.Querier) error) error

// Apply runs the given migrations in order, each inside its own transaction
// via runTx. Migrations whose version is already in the tracking table are
// skipped. Returns the versions actually applied and the versions that were
// skipped (already-applied), in order.
//
// On the first failure, returns the partial applied/skipped lists plus the
// error — the caller can report which migrations succeeded before the failure.
func Apply(ctx context.Context, runTx TxRunner, fsys fs.FS, migrations []Migration) (applied, skipped []string, err error) {
	if len(migrations) == 0 {
		return nil, nil, nil
	}

	// Establish the tracking table and read what's already applied. Both
	// happen in their own short transaction so the per-migration loop below
	// has a fresh starting point.
	var existing map[string]bool
	err = runTx(ctx, func(q db.Querier) error {
		if err := EnsureTrackingTable(ctx, q); err != nil {
			return err
		}
		got, err := AppliedVersions(ctx, q)
		if err != nil {
			return err
		}
		existing = got
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	applied = make([]string, 0, len(migrations))
	skipped = make([]string, 0, len(migrations))
	for _, mig := range migrations {
		if existing[mig.Version] {
			skipped = append(skipped, mig.Version)
			continue
		}
		if err := runOneUp(ctx, runTx, fsys, mig); err != nil {
			return applied, skipped, fmt.Errorf("mirrorstack/migration: apply %s: %w", mig.Version, err)
		}
		applied = append(applied, mig.Version)
	}
	return applied, skipped, nil
}

// ApplyDown runs the .down.sql for each migration in the order given (callers
// reverse the slice for downgrade). Each runs in its own transaction. The
// tracking table entry is removed on success. Returns an error naming the
// missing version if any migration has no down file.
func ApplyDown(ctx context.Context, runTx TxRunner, fsys fs.FS, migrations []Migration) (reverted []string, err error) {
	if len(migrations) == 0 {
		return nil, nil
	}

	// Verify all migrations have down files BEFORE running any of them.
	// Half-applied downgrades are worse than failed-fast.
	for _, mig := range migrations {
		if mig.DownFile == "" {
			return nil, fmt.Errorf("mirrorstack/migration: missing down file for version %s", mig.Version)
		}
	}

	// Ensure the tracking table exists (defensive — downgrade against a
	// never-installed module is a no-op rather than a missing-table error).
	if err := runTx(ctx, func(q db.Querier) error {
		return EnsureTrackingTable(ctx, q)
	}); err != nil {
		return nil, err
	}

	reverted = make([]string, 0, len(migrations))
	for _, mig := range migrations {
		if err := runOneDown(ctx, runTx, fsys, mig); err != nil {
			return reverted, fmt.Errorf("mirrorstack/migration: revert %s: %w", mig.Version, err)
		}
		reverted = append(reverted, mig.Version)
	}
	return reverted, nil
}

// runOneUp executes a single migration's up SQL plus the tracking insert
// inside one transaction. The file is read BEFORE the transaction starts so
// we don't hold a pooled DB connection during filesystem I/O — relevant for
// fs.FS implementations backed by network storage (S3, etc.).
func runOneUp(ctx context.Context, runTx TxRunner, fsys fs.FS, mig Migration) error {
	content, err := fs.ReadFile(fsys, mig.UpFile)
	if err != nil {
		return fmt.Errorf("read %s: %w", mig.UpFile, err)
	}
	sql := string(content)
	return runTx(ctx, func(q db.Querier) error {
		if _, err := q.Exec(ctx, sql); err != nil {
			return fmt.Errorf("exec migration sql: %w", err)
		}
		if _, err := q.Exec(ctx,
			"INSERT INTO __mirrorstack_migrations (version, name) VALUES ($1, $2)",
			mig.Version, mig.Name,
		); err != nil {
			return fmt.Errorf("record migration: %w", err)
		}
		return nil
	})
}

// runOneDown executes a single migration's down SQL plus the tracking delete
// inside one transaction. File read happens before the transaction (see runOneUp).
func runOneDown(ctx context.Context, runTx TxRunner, fsys fs.FS, mig Migration) error {
	content, err := fs.ReadFile(fsys, mig.DownFile)
	if err != nil {
		return fmt.Errorf("read %s: %w", mig.DownFile, err)
	}
	sql := string(content)
	return runTx(ctx, func(q db.Querier) error {
		if _, err := q.Exec(ctx, sql); err != nil {
			return fmt.Errorf("exec down sql: %w", err)
		}
		if _, err := q.Exec(ctx,
			"DELETE FROM __mirrorstack_migrations WHERE version = $1",
			mig.Version,
		); err != nil {
			return fmt.Errorf("remove migration record: %w", err)
		}
		return nil
	})
}

// Slice returns the subset of migrations strictly after fromVersion and up to
// (and including) toVersion, sorted ascending by version. Used by Apply for
// upgrades. Returns an empty slice if there's no work to do (toVersion <=
// fromVersion or both unknown).
//
// fromVersion may be "" (treated as "before all migrations") so install can
// share this function — Slice("", "0008") returns everything up to 0008.
//
// An unknown toVersion that does not appear in migrations returns an error
// because the caller asked to upgrade to a version this module doesn't have.
func Slice(migrations []Migration, fromVersion, toVersion string) ([]Migration, error) {
	fromN, err := versionInt(fromVersion, -1)
	if err != nil {
		return nil, err
	}
	toN, err := versionInt(toVersion, -1)
	if err != nil {
		return nil, err
	}
	if toVersion != "" && !versionExists(migrations, toVersion) {
		return nil, fmt.Errorf("unknown target version %s", toVersion)
	}

	out := make([]Migration, 0)
	for _, m := range migrations {
		mn, err := strconv.Atoi(m.Version)
		if err != nil {
			continue
		}
		if mn > fromN && (toN == -1 || mn <= toN) {
			out = append(out, m)
		}
	}
	return out, nil
}

// SliceDown returns migrations to revert when going from fromVersion down to
// toVersion (where toVersion < fromVersion), sorted descending so each is
// applied newest-first. Both versions must exist in migrations.
func SliceDown(migrations []Migration, fromVersion, toVersion string) ([]Migration, error) {
	fromN, err := versionInt(fromVersion, -1)
	if err != nil {
		return nil, err
	}
	toN, err := versionInt(toVersion, -1)
	if err != nil {
		return nil, err
	}
	if fromN <= toN {
		return nil, fmt.Errorf("downgrade requires from > to (got from=%s to=%s)", fromVersion, toVersion)
	}
	if !versionExists(migrations, fromVersion) {
		return nil, fmt.Errorf("unknown current version %s", fromVersion)
	}

	out := make([]Migration, 0)
	for i := len(migrations) - 1; i >= 0; i-- {
		mn, err := strconv.Atoi(migrations[i].Version)
		if err != nil {
			continue
		}
		if mn > toN && mn <= fromN {
			out = append(out, migrations[i])
		}
	}
	return out, nil
}

// versionInt parses a version string into an int. Empty input returns the
// fallback (used for "" → -1, meaning "before all migrations").
func versionInt(v string, empty int) (int, error) {
	if v == "" {
		return empty, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid version %q: not numeric", v)
	}
	return n, nil
}

func versionExists(migrations []Migration, v string) bool {
	for _, m := range migrations {
		if m.Version == v {
			return true
		}
	}
	return false
}
