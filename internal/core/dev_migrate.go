package core

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/migration"
	"github.com/mirrorstack-ai/app-module-sdk/internal/runtime"
)

// devAppSchema is the schema app-scope migrations target in dev. There is no
// per-app tenant locally, so every app migration shares one schema — public,
// which is also on the default search_path m.DB/m.Tx fall back to at runtime
// when no app schema is set, so handlers read the tables migrations create.
const devAppSchema = "public"

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
// per-module tracking table mod_<id>.schema_migrations records what ran, keyed
// by (scope, version). Per-module (not a shared table) so two modules sharing
// one dev Postgres don't collide on the same version number.
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

	modSchema := pgx.Identifier{"mod_" + m.config.ID}.Sanitize()
	if _, err := pool.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS `+modSchema); err != nil {
		return fmt.Errorf("dev migrate: create mod schema: %w", err)
	}

	// The tracking table lives in the module's OWN schema and MUST be
	// per-module. Every module on a developer's machine shares one dev
	// Postgres, so the old shared __ms_local.schema_migrations (keyed only by
	// scope+version) collided across modules: module B's "app/0001" was
	// skipped because module A had already recorded "app/0001", so B's tables
	// were never created. Keyed per mod_<id> schema, each module's version
	// space is independent.
	trackingTable := pgx.Identifier{"mod_" + m.config.ID, "schema_migrations"}.Sanitize()
	if _, err := pool.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			scope      text NOT NULL,
			version    text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now(),
			PRIMARY KEY (scope, version)
		)`, trackingTable)); err != nil {
		return fmt.Errorf("dev migrate: create tracking table: %w", err)
	}

	for _, scope := range migration.AllScopes() {
		if err := m.applyDevScope(ctx, pool, trackingTable, scope); err != nil {
			return err
		}
	}
	return nil
}

// applyDevScope filters the embedded sql/<scope>/ migrations down to those
// not already recorded in the tracking table, then applies the remainder
// via the same TxRunner the production install handler uses.
func (m *Module) applyDevScope(ctx context.Context, pool *pgxpool.Pool, trackingTable string, scope migration.Scope) error {
	all, err := migration.List(m.config.SQL, scope)
	if err != nil {
		return fmt.Errorf("dev migrate %s: list: %w", scope, err)
	}
	if len(all) == 0 {
		return nil
	}

	applied, err := loadAppliedVersions(ctx, pool, trackingTable, scope)
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

	// App-scope migrations carry no per-app tenant in dev, so m.Tx would run
	// each with an empty schema and fall back to the connection's default
	// search_path. That default is not guaranteed identical across the pooled
	// connections used for consecutive migrations, so a migration referencing
	// an earlier one's table can fail with "relation does not exist". Pin the
	// whole app sequence to one schema (public) so every migration lands in the
	// same place. Module scope uses m.ModuleTx, which sets mod_<id> itself.
	migrateCtx := ctx
	if scope == migration.ScopeApp {
		migrateCtx = db.WithSchema(ctx, devAppSchema)
	}

	runTx := m.lifecycleTxRunner(scope)
	ran, err := migration.Apply(migrateCtx, runTx, m.config.SQL, pending)
	// Record whatever ran before the failure (if any) so retries skip them.
	if recErr := recordAppliedVersions(ctx, pool, trackingTable, scope, ran); recErr != nil && err == nil {
		return fmt.Errorf("dev migrate %s: record applied: %w", scope, recErr)
	}
	if err != nil {
		return fmt.Errorf("dev migrate %s: apply: %w", scope, err)
	}
	m.logger.Printf("dev: applied %d %s migration(s): %v", len(ran), scope, ran)
	return nil
}

func loadAppliedVersions(ctx context.Context, pool *pgxpool.Pool, trackingTable string, scope migration.Scope) (map[string]bool, error) {
	rows, err := pool.Query(ctx,
		fmt.Sprintf(`SELECT version FROM %s WHERE scope = $1`, trackingTable),
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

func recordAppliedVersions(ctx context.Context, pool *pgxpool.Pool, trackingTable string, scope migration.Scope, versions []string) error {
	for _, v := range versions {
		if _, err := pool.Exec(ctx,
			fmt.Sprintf(`INSERT INTO %s (scope, version) VALUES ($1, $2)
			 ON CONFLICT DO NOTHING`, trackingTable),
			string(scope), v); err != nil {
			return err
		}
	}
	return nil
}
