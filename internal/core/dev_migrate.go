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

// applyDevMigrations applies MODULE-scope migrations into mod_<id> at startup.
//
// App-scope migrations are deliberately NOT run here: there is no app at boot
// time. They are provisioned lazily, once per app, on the first request that
// carries an app identity — see Module.ensureDevAppSchema. This mirrors
// production, where each app gets its own app_<id> schema (the platform's
// install lifecycle creates and migrates it), instead of every app sharing one
// schema locally. Sharing one schema is exactly what broke per-app isolation in
// dev: two apps installing the same module read and wrote the same tables.
//
// Module scope is shared across apps by design, so mod_<id> has a home the
// moment the module boots and is migrated here.
func (m *Module) applyDevMigrations(ctx context.Context) error {
	return m.ensureSchemaMigrated(ctx, migration.ScopeModule, "mod_"+m.config.ID)
}

// ensureDevAppSchema lazily creates + migrates the per-app schema app_<id> the
// first time a request for that app arrives in dev. Idempotent and cached per
// process: only the first request for a given schema pays the migration cost;
// later requests return the recorded result without touching Postgres. A
// per-schema sync.Once also serializes concurrent first-touch requests so the
// migrations run exactly once.
func (m *Module) ensureDevAppSchema(ctx context.Context, schema string) error {
	v, _ := m.devProvision.LoadOrStore(schema, &devProvisionEntry{})
	entry := v.(*devProvisionEntry)
	entry.once.Do(func() { entry.err = m.provisionDevAppSchema(ctx, schema) })
	return entry.err
}

// provisionDevAppSchema runs the per-app setup: app-scope migrations into
// app_<id>, then the contributions table (when the module declares any slots),
// since that store is per-app — handlers read it via Module.DB, which resolves
// to the app schema. Without it, a host module's slot reads/writes would hit a
// missing relation the moment dev moved off the single shared schema.
func (m *Module) provisionDevAppSchema(ctx context.Context, schema string) error {
	if err := m.ensureSchemaMigrated(ctx, migration.ScopeApp, schema); err != nil {
		return err
	}
	if m.contribReg.Len() > 0 {
		sctx := db.WithSchema(ctx, schema)
		q, release, err := m.DB(sctx)
		if err != nil {
			return fmt.Errorf("dev provision %s: open db: %w", schema, err)
		}
		defer release()
		if err := m.contribStorage.EnsureTable(sctx, q); err != nil {
			return fmt.Errorf("dev provision %s: ensure contributions table: %w", schema, err)
		}
	}
	return nil
}

// ensureSchemaMigrated creates the target schema and its per-schema migration
// tracking table, then applies any not-yet-recorded migrations for the given
// scope. Each schema owns its own schema_migrations table (keyed by
// scope+version) so every app's app-scope history is independent and a second
// app installing the same module re-runs the migrations into its own schema
// instead of being skipped as "already applied".
func (m *Module) ensureSchemaMigrated(ctx context.Context, scope migration.Scope, schema string) error {
	pool, release, err := m.resolvePool(ctx)
	if err != nil {
		return fmt.Errorf("dev migrate: open pool: %w", err)
	}
	defer release()

	schemaIdent := pgx.Identifier{schema}.Sanitize()
	if _, err := pool.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS `+schemaIdent); err != nil {
		return fmt.Errorf("dev migrate: create schema %s: %w", schema, err)
	}

	trackingTable := pgx.Identifier{schema, "schema_migrations"}.Sanitize()
	if _, err := pool.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			scope      text NOT NULL,
			version    text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now(),
			PRIMARY KEY (scope, version)
		)`, trackingTable)); err != nil {
		return fmt.Errorf("dev migrate: create tracking table in %s: %w", schema, err)
	}

	return m.applyDevScope(ctx, pool, trackingTable, schema, scope)
}

// applyDevScope filters the embedded sql/<scope>/ migrations down to those not
// already recorded in the schema's tracking table, then applies the remainder
// via the same TxRunner the production install handler uses, pinned to the
// target schema.
func (m *Module) applyDevScope(ctx context.Context, pool *pgxpool.Pool, trackingTable, schema string, scope migration.Scope) error {
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

	// Pin the whole sequence to the target schema so every migration lands in
	// the same place. App scope runs via Module.Tx, which reads this schema from
	// the context; module scope runs via Module.ModuleTx, which overlays
	// mod_<id> itself (identical to schema here) — setting it is harmless.
	migrateCtx := db.WithSchema(ctx, schema)
	runTx := m.lifecycleTxRunner(scope)
	ran, err := migration.Apply(migrateCtx, runTx, m.config.SQL, pending)
	// Record whatever ran before the failure (if any) so retries skip them.
	if recErr := recordAppliedVersions(ctx, pool, trackingTable, scope, ran); recErr != nil && err == nil {
		return fmt.Errorf("dev migrate %s: record applied: %w", scope, recErr)
	}
	if err != nil {
		return fmt.Errorf("dev migrate %s: apply: %w", scope, err)
	}
	m.logger.Printf("dev: applied %d %s migration(s) to %s: %v", len(ran), scope, schema, ran)
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
