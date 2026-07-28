package core

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

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
	if err := m.ensureSchemaMigrated(ctx, migration.ScopeModule, "mod_"+m.config.ID); err != nil {
		return err
	}
	// Publish this module's identity + exposure set for co-located consumers,
	// then hold the lease open for the life of the process (see
	// dev_directory.go). NON-FATAL by design: the directory is an optimization
	// that lets a co-located cross-module read stay local. A module that cannot
	// publish still boots and serves normally; its consumers simply keep taking
	// the read-exposed proxy path, which is exactly the behavior that existed
	// before the directory. Failing boot over a degraded optimization would
	// trade a working module for a faster one.
	//
	// Note the shape: NO early return, and no `return nil` swallowing a setup
	// failure. Whether the table could be created and whether this module's row
	// could be written are separate questions with separate answers, and the
	// first failing is the WEAKEST possible evidence that the second will —
	// under `mirrorstack dev --all` the usual cause of a failed create is a peer
	// module creating the same table at the same instant. publishDevDirectory
	// owns that ordering and the lease goroutine owns the retry.
	// Bounded so "NON-FATAL by design" is true of the BOOT PATH too, not just of
	// the outcome. The first thing the publish does is take a
	// pg_advisory_xact_lock, which blocks until the holder's transaction ends;
	// applyDevMigrations runs synchronously on the way to ListenAndServe, so a
	// peer wedged mid-DDL (a debugger breakpoint, a SIGSTOPped process) would
	// otherwise stop this module from ever serving, with no log line and no
	// timeout — the hardest possible failure to diagnose. A deadline turns that
	// into one degraded publish plus a log line, and the 30s heartbeat retries.
	bootCtx, cancel := context.WithTimeout(ctx, devDirectoryBootTimeout)
	defer cancel()
	m.startDevDirectoryLease(bootCtx)
	return nil
}

// devProvisionEntry guards one app schema's lazy provisioning. The sync.Once
// serializes concurrent first-touch requests; err caches the outcome.
type devProvisionEntry struct {
	once sync.Once
	err  error
}

// ensureDevAppSchema lazily creates + migrates the per-app schema app_<id> the
// first time a request for that app arrives in dev. Idempotent and cached per
// process: only the first request for a given schema pays the migration cost;
// later requests return the recorded result without touching Postgres. A
// per-schema sync.Once also serializes concurrent first-touch requests so the
// migrations run exactly once.
func (m *Module) ensureDevAppSchema(ctx context.Context, schema string) error {
	// The loaded bool is intentionally discarded: LoadOrStore returns the
	// canonical entry whether we created it or won the race for an existing
	// one, and sync.Once makes provisioning run exactly once regardless.
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
// module_id+scope+version) so every app's app-scope history is independent
// AND two modules sharing one app schema do not collide on the (scope,version)
// key — a second app, or a second module in the same app, re-runs its own
// migrations into the target schema instead of being skipped as "already
// applied".
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
	if err := ensureTrackingTable(ctx, pool, trackingTable); err != nil {
		return fmt.Errorf("dev migrate: tracking table in %s: %w", schema, err)
	}

	return m.applyDevScope(ctx, pool, trackingTable, schema, scope)
}

// ensureTrackingTable creates the per-schema schema_migrations table and, for
// schemas created by an older SDK, upgrades the legacy (scope, version) layout
// to the module-scoped (module_id, scope, version) layout.
//
// Legacy rows are backfilled with module_id set to the empty string (the
// "unknown owner" marker): no live module ever records under the empty id, so
// on the next dev run every module's applied-set comes up empty for those
// versions, drift detection finds its tables missing, and the migrations
// re-apply into the schema (cleanly, because module migrations use CREATE
// TABLE IF NOT EXISTS). This is how the recorded-but-never-applied schemas
// self-heal — see applyDevScope.
func ensureTrackingTable(ctx context.Context, pool *pgxpool.Pool, trackingTable string) error {
	if _, err := pool.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			module_id  text NOT NULL DEFAULT '',
			scope      text NOT NULL,
			version    text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now(),
			PRIMARY KEY (module_id, scope, version)
		)`, trackingTable)); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	// Upgrade a legacy table (PK was (scope, version), no module_id column).
	if _, err := pool.Exec(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS module_id text NOT NULL DEFAULT ''`,
		trackingTable)); err != nil {
		return fmt.Errorf("add module_id column: %w", err)
	}
	// Rebuild the PK to include module_id when the table predates it. The
	// constraint name is schema-local and stable, so DROP-then-ADD is safe and
	// idempotent across runs.
	if _, err := pool.Exec(ctx, fmt.Sprintf(
		`DO $$
		 DECLARE pk text;
		 BEGIN
		   SELECT conname INTO pk FROM pg_constraint
		   WHERE conrelid = %s::regclass AND contype = 'p';
		   IF pk IS NOT NULL AND NOT EXISTS (
		     SELECT 1 FROM pg_attribute a
		     JOIN pg_constraint c ON c.conrelid = a.attrelid AND a.attnum = ANY (c.conkey)
		     WHERE c.conname = pk AND a.attname = 'module_id'
		   ) THEN
		     EXECUTE 'ALTER TABLE %s DROP CONSTRAINT ' || quote_ident(pk);
		     EXECUTE 'ALTER TABLE %s ADD PRIMARY KEY (module_id, scope, version)';
		   END IF;
		 END $$`,
		quoteLiteral(trackingTable), trackingTable, trackingTable)); err != nil {
		return fmt.Errorf("rebuild primary key: %w", err)
	}
	return nil
}

// applyDevScope filters the embedded sql/<scope>/ migrations down to those not
// already recorded in the schema's tracking table FOR THIS MODULE, then applies
// the remainder via the same TxRunner the production install handler uses,
// pinned to the target schema.
//
// Self-heal: a version recorded as applied but whose effect is missing from the
// schema (no table carrying this module's id prefix exists) is drift — the
// historical search_path bug recorded the row while the DDL leaked into public.
// Such recordings are discarded so the migrations re-apply into the right
// schema on this run.
func (m *Module) applyDevScope(ctx context.Context, pool *pgxpool.Pool, trackingTable, schema string, scope migration.Scope) error {
	all, err := migration.List(m.config.SQL, scope)
	if err != nil {
		return fmt.Errorf("dev migrate %s: list: %w", scope, err)
	}
	if len(all) == 0 {
		return nil
	}

	applied, err := m.loadAppliedVersions(ctx, pool, trackingTable, scope)
	if err != nil {
		return fmt.Errorf("dev migrate %s: load applied: %w", scope, err)
	}

	// Drift detection: if anything is recorded for this module but none of its
	// tables exist in the schema, the recorded DDL never landed here (the public
	// leak). Forget the recordings so every migration re-applies; clear the stale
	// rows so the tracking table reflects reality after the re-apply.
	if len(applied) > 0 {
		present, err := m.moduleHasTables(ctx, pool, schema)
		if err != nil {
			return fmt.Errorf("dev migrate %s: drift check: %w", scope, err)
		}
		if !present {
			m.logger.Printf("dev: %s schema %s has tracking rows for module %s but no tables — re-applying (search_path drift self-heal)", scope, schema, m.config.ID)
			if err := m.clearAppliedVersions(ctx, pool, trackingTable, scope); err != nil {
				return fmt.Errorf("dev migrate %s: clear drift: %w", scope, err)
			}
			applied = map[string]bool{}
		}
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

	// Pin the whole sequence to the target schema two ways: db.WithSchema sets it
	// in context (the path Module.Tx/ModuleTx read), and pinnedRunTx forces a
	// SET LOCAL search_path inside each migration's transaction. The explicit pin
	// is defense-in-depth: it guarantees the DDL lands in the app schema even if
	// the context-based path ever regresses — which is the exact failure that
	// once leaked module tables into public.
	migrateCtx := db.WithSchema(ctx, schema)
	runTx := pinnedRunTx(m.lifecycleTxRunner(scope), schema)
	ran, err := migration.Apply(migrateCtx, runTx, m.config.SQL, pending)
	// Record whatever ran before the failure (if any) so retries skip them.
	if recErr := m.recordAppliedVersions(ctx, pool, trackingTable, scope, ran); recErr != nil && err == nil {
		return fmt.Errorf("dev migrate %s: record applied: %w", scope, recErr)
	}
	if err != nil {
		return fmt.Errorf("dev migrate %s: apply: %w", scope, err)
	}
	m.logger.Printf("dev: applied %d %s migration(s) to %s: %v", len(ran), scope, schema, ran)
	return nil
}

// pinnedRunTx wraps a TxRunner so the wrapped fn runs with search_path pinned
// to schema for the duration of the transaction. SET LOCAL is cleared on
// COMMIT/ROLLBACK, so it cannot leak to a later use of the pooled connection.
// The pin runs inside the same transaction as the DDL, so even an empty
// migration context (no db.WithSchema) still lands the DDL in the app schema.
func pinnedRunTx(inner migration.TxRunner, schema string) migration.TxRunner {
	pin := "SET LOCAL search_path TO " + pgx.Identifier{schema}.Sanitize()
	return func(ctx context.Context, fn func(q db.Querier) error) error {
		return inner(ctx, func(q db.Querier) error {
			if _, err := q.Exec(ctx, pin); err != nil {
				return fmt.Errorf("pin search_path to %s: %w", schema, err)
			}
			return fn(q)
		})
	}
}

// moduleHasTables reports whether the schema contains at least one ordinary
// table whose name starts with this module's id prefix (m<id>_...). Used by the
// drift self-heal: a module that recorded migrations but owns no tables in the
// schema never actually ran its DDL there.
func (m *Module) moduleHasTables(ctx context.Context, pool *pgxpool.Pool, schema string) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = $1
			  AND table_type = 'BASE TABLE'
			  AND table_name LIKE $2
		)`, schema, m.config.ID+"\\_%").Scan(&exists)
	return exists, err
}

func (m *Module) loadAppliedVersions(ctx context.Context, pool *pgxpool.Pool, trackingTable string, scope migration.Scope) (map[string]bool, error) {
	rows, err := pool.Query(ctx,
		fmt.Sprintf(`SELECT version FROM %s WHERE module_id = $1 AND scope = $2`, trackingTable),
		m.config.ID, string(scope))
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

func (m *Module) recordAppliedVersions(ctx context.Context, pool *pgxpool.Pool, trackingTable string, scope migration.Scope, versions []string) error {
	for _, v := range versions {
		if _, err := pool.Exec(ctx,
			fmt.Sprintf(`INSERT INTO %s (module_id, scope, version) VALUES ($1, $2, $3)
			 ON CONFLICT DO NOTHING`, trackingTable),
			m.config.ID, string(scope), v); err != nil {
			return err
		}
	}
	return nil
}

// clearAppliedVersions removes this module's recorded rows for the scope. Used
// by the drift self-heal before a full re-apply so the tracking table is
// rewritten to match what actually runs this time. Legacy rows under the empty
// "unknown owner" id for the same versions are also cleared so the re-applied
// rows are not shadowed by a duplicate version under a different module_id.
func (m *Module) clearAppliedVersions(ctx context.Context, pool *pgxpool.Pool, trackingTable string, scope migration.Scope) error {
	_, err := pool.Exec(ctx,
		fmt.Sprintf(`DELETE FROM %s WHERE scope = $1 AND module_id IN ($2, '')`, trackingTable),
		string(scope), m.config.ID)
	return err
}

// quoteLiteral wraps s as a single-quoted SQL string literal (doubling embedded
// quotes) for use where a value — not an identifier — is interpolated into DDL,
// such as the regclass cast inside the tracking-table PK upgrade.
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
