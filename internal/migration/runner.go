package migration

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"sort"
	"strconv"

	"github.com/mirrorstack-ai/app-module-sdk/db"
)

// Migration represents one migration on disk: an up-file with a numeric
// version prefix, an optional matching down-file, and the human-readable slug.
type Migration struct {
	Version  string // numeric prefix from filename, e.g., "0008"
	Name     string // human-readable slug, e.g., "add_index"
	UpFile   string // path within fsys, e.g., "sql/app/0008_add_index.up.sql"
	DownFile string // path within fsys, "" if no down file present
}

// List returns all migrations from sql/{scope}/, sorted by numeric version
// (ascending). A migration is identified by its .up.sql file; the matching
// .down.sql is recorded if present (empty otherwise — downgrades will fail
// for that version).
//
// Returns an empty slice (not an error) if fsys is nil or sql/{scope}/ does
// not exist. A module that has only app migrations (no cross-app shared
// state) will see List(fsys, ScopeModule) return an empty slice cleanly,
// which is what the manifest needs to report version "" for that scope.
func List(fsys fs.FS, scope Scope) ([]Migration, error) {
	if fsys == nil {
		return nil, nil
	}
	dir := scope.Dir()
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("mirrorstack/migration: read %s dir: %w", dir, err)
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
			upFiles[version] = dir + "/" + e.Name()
			names[version] = slug
			continue
		}
		if m := downFilePattern.FindStringSubmatch(e.Name()); m != nil {
			downFiles[m[1]] = dir + "/" + e.Name()
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

// TxRunner runs fn inside a database transaction. Matches Module.Tx signature
// so handlers can pass it directly without leaking pgxpool out of the system
// package.
type TxRunner func(ctx context.Context, fn func(q db.Querier) error) error

// Apply runs the given migrations in order, each inside its own transaction
// via runTx. Returns the versions actually applied.
//
// The SDK is a stateless executor: it does NOT maintain a tracking table and
// does NOT skip already-applied migrations. The platform owns the "which
// versions are installed where" state in its control plane and decides the
// exact {from, to} range to pass through the lifecycle endpoint. If the
// platform asks to re-run a migration that was already applied, Postgres will
// surface the duplicate error (e.g., "relation already exists") — that's a
// platform bug, not an SDK bug.
//
// On the first failure, returns the partial applied list plus the error so
// the platform can update its own state with what actually ran.
//
// Known limitation: each migration's SQL runs inside a transaction. Postgres
// statements that cannot run inside a transaction block — CREATE INDEX
// CONCURRENTLY, VACUUM, CLUSTER, REINDEX CONCURRENTLY, ALTER SYSTEM — will
// fail with an opaque error. Split such operations into a separate migration
// or document the limitation in the module's README.
func Apply(ctx context.Context, runTx TxRunner, fsys fs.FS, migrations []Migration) (applied []string, err error) {
	if len(migrations) == 0 {
		return nil, nil
	}

	applied = make([]string, 0, len(migrations))
	for _, mig := range migrations {
		if err := runOneUp(ctx, runTx, fsys, mig); err != nil {
			return applied, fmt.Errorf("mirrorstack/migration: apply %s: %w", mig.Version, err)
		}
		applied = append(applied, mig.Version)
	}
	return applied, nil
}

// ApplyDown runs the .down.sql for each migration in the order given (callers
// reverse the slice for downgrade). Each runs in its own transaction.
//
// Pre-validates that every migration has a down file BEFORE running any SQL —
// half-applied downgrades are worse than failing fast on a missing file.
//
// Like Apply, this is stateless: the caller is responsible for passing the
// right slice of migrations to revert. The SDK does not consult any tracking
// table to verify the versions were previously applied. If the platform asks
// to revert a migration that never ran, the down SQL will fail with a
// Postgres error.
func ApplyDown(ctx context.Context, runTx TxRunner, fsys fs.FS, migrations []Migration) (reverted []string, err error) {
	if len(migrations) == 0 {
		return nil, nil
	}

	// Pre-check: every migration has a down file.
	for _, mig := range migrations {
		if mig.DownFile == "" {
			return nil, fmt.Errorf("mirrorstack/migration: missing down file for version %s", mig.Version)
		}
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

// runOneUp executes a single migration's up SQL inside one transaction. The
// file is read BEFORE the transaction starts so we don't hold a pooled DB
// connection during filesystem I/O — relevant for fs.FS implementations
// backed by network storage (S3, etc.).
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
		return nil
	})
}

// runOneDown executes a single migration's down SQL inside one transaction.
// File read happens before the transaction (see runOneUp).
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

	out := make([]Migration, 0, len(migrations))
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
// applied newest-first. Both versions must be real migration numbers — unlike
// Slice, there is no "" special case for "before everything" because a
// downgrade always has a known current version (the caller is reversing
// something that was installed).
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

	out := make([]Migration, 0, len(migrations))
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
	return slices.ContainsFunc(migrations, func(m Migration) bool {
		return m.Version == v
	})
}
