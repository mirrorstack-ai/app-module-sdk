package core

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mirrorstack-ai/app-module-sdk/internal/migration"
	"github.com/mirrorstack-ai/app-module-sdk/internal/runtime"
)

// devMigrateEnvVar is the local-dev signal set by `mirrorstack dev`. When
// non-empty, the module is running under the CLI's dev lifecycle and applies
// its embedded migrations on every start so the developer's local Postgres
// matches the module's source tree.
//
// Independent of DATABASE_URL — that one is a generic fallback honored by
// db.Open() but does NOT trigger auto-apply (it's used for non-CLI flows like
// `go test` against a personal Postgres where the developer owns migration
// timing).
const devMigrateEnvVar = "MS_LOCAL_DB_URL"

// devMigrateEnabled returns true when ms.Start() should auto-apply migrations
// before serving. False in Lambda + task-worker modes (production credentials
// arrive per-invocation) and when the dev env var is unset.
func (m *Module) devMigrateEnabled() bool {
	if runtime.IsLambda() || runtime.IsTaskWorker() {
		return false
	}
	return os.Getenv(devMigrateEnvVar) != ""
}

// applyDevMigrations runs every embedded migration that has not yet been
// applied to the dev Postgres. Idempotent across module restarts — the
// tracking table __ms_local.schema_migrations records what ran, keyed by
// (scope, version).
//
// Pre-creates the mod_<id> schema so module-scope migrations have a home;
// app-scope migrations land in whatever schema the connection sees by
// default (public in dev — there is no real per-app tenant). Production
// install lifecycle handles both with proper schemas; this is the dev
// shortcut.
func (m *Module) applyDevMigrations(ctx context.Context) error {
	pool, release, err := m.resolvePool(ctx)
	if err != nil {
		return fmt.Errorf("dev migrate: open pool: %w", err)
	}
	defer release()

	if _, err := pool.Exec(ctx, fmt.Sprintf(
		`CREATE SCHEMA IF NOT EXISTS %s`,
		pgx.Identifier{"mod_" + m.config.ID}.Sanitize(),
	)); err != nil {
		return fmt.Errorf("dev migrate: create mod schema: %w", err)
	}

	if _, err := pool.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS __ms_local;
		CREATE TABLE IF NOT EXISTS __ms_local.schema_migrations (
			scope      text NOT NULL,
			version    text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now(),
			PRIMARY KEY (scope, version)
		)`); err != nil {
		return fmt.Errorf("dev migrate: create tracking table: %w", err)
	}

	for _, scope := range migration.AllScopes() {
		if err := m.applyDevScope(ctx, pool, scope); err != nil {
			return err
		}
	}
	return nil
}

// applyDevScope filters the embedded sql/<scope>/ migrations down to those
// not already recorded in the tracking table, then applies the remainder
// via the same TxRunner the production install handler uses.
func (m *Module) applyDevScope(ctx context.Context, pool *pgxpool.Pool, scope migration.Scope) error {
	all, err := migration.List(m.config.SQL, scope)
	if err != nil {
		return fmt.Errorf("dev migrate %s: list: %w", scope, err)
	}
	if len(all) == 0 {
		return nil
	}

	applied, err := loadAppliedVersions(ctx, pool, scope)
	if err != nil {
		return fmt.Errorf("dev migrate %s: load applied: %w", scope, err)
	}

	pending := make([]migration.Migration, 0, len(all))
	for _, mig := range all {
		if !applied[mig.Version] {
			pending = append(pending, mig)
		}
	}
	if len(pending) == 0 {
		return nil
	}

	runTx := m.lifecycleTxRunner(scope)
	ran, err := migration.Apply(ctx, runTx, m.config.SQL, pending)
	// Record whatever ran before the failure (if any) so retries skip them.
	if recErr := recordAppliedVersions(ctx, pool, scope, ran); recErr != nil && err == nil {
		return fmt.Errorf("dev migrate %s: record applied: %w", scope, recErr)
	}
	if err != nil {
		return fmt.Errorf("dev migrate %s: apply: %w", scope, err)
	}
	m.logger.Printf("dev: applied %d %s migration(s): %v", len(ran), scope, ran)
	return nil
}

func loadAppliedVersions(ctx context.Context, pool *pgxpool.Pool, scope migration.Scope) (map[string]bool, error) {
	rows, err := pool.Query(ctx,
		`SELECT version FROM __ms_local.schema_migrations WHERE scope = $1`,
		string(scope))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = true
	}
	return out, rows.Err()
}

func recordAppliedVersions(ctx context.Context, pool *pgxpool.Pool, scope migration.Scope, versions []string) error {
	for _, v := range versions {
		if _, err := pool.Exec(ctx,
			`INSERT INTO __ms_local.schema_migrations (scope, version) VALUES ($1, $2)
			 ON CONFLICT DO NOTHING`,
			string(scope), v); err != nil {
			return err
		}
	}
	return nil
}
