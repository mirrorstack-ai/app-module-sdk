package core

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mirrorstack-ai/app-module-sdk/audit"
	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/auditstate"
	"github.com/mirrorstack-ai/app-module-sdk/internal/migration"
	"github.com/mirrorstack-ai/app-module-sdk/internal/runtime"
	"github.com/mirrorstack-ai/app-module-sdk/system"
)

// DB returns a scoped database connection.
//
// Production: uses per-app credentials injected by the platform via Lambda payload.
// Dev: uses DATABASE_URL env var with localhost fallback. Dev connections are
// wrapped with the cross-module fail-fast guard (db_guard.go): a raw query
// against another module's tables errors immediately instead of silently
// succeeding in the shared dev database — use ms.DependencyDB for that.
//
//	conn, release, err := mod.DB(r.Context())
//	if err != nil { ... }
//	defer release()
//	conn.QueryRow(ctx, "SELECT ...").Scan(&v)
func (m *Module) DB(ctx context.Context) (db.Querier, func(), error) {
	pool, releasePool, err := m.resolvePool(ctx)
	if err != nil {
		return nil, nil, err
	}
	querier, releaseConn, err := db.AcquireScoped(ctx, pool)
	if err != nil {
		releasePool()
		return nil, nil, err
	}
	return m.devGuardFor(ctx, m.withModuleID(querier), db.CredentialFrom), func() {
		releaseConn()
		releasePool()
	}, nil
}

// Tx runs fn inside a transaction with schema isolation. Commits on success,
// rolls back on error. The app schema is read from the caller's context (set
// by the platform's Lambda invoke shim via db.WithSchema). Compare with
// Module.ModuleTx which explicitly overlays the mod_<id> schema.
//
//	err := mod.Tx(r.Context(), func(q db.Querier) error {
//	    queries := generated.New(q)
//	    item, err := queries.GetItem(ctx, id)
//	    if err != nil { return err }
//	    return queries.DeductBalance(ctx, params)
//	})
func (m *Module) Tx(ctx context.Context, fn func(q db.Querier) error) error {
	pool, releasePool, err := m.resolvePool(ctx)
	if err != nil {
		return err
	}
	defer releasePool()
	var recordedAudit bool
	err = db.Tx(ctx, pool, func(q db.Querier) error {
		tracked := auditstate.Track(m.devGuardFor(ctx, m.withModuleID(q), db.CredentialFrom))
		err := fn(tracked)
		recordedAudit = tracked.Recorded()
		return err
	})
	if err != nil {
		return err
	}
	if recordedAudit {
		m.drainAuditAfterCommit(ctx)
	}
	return nil
}

// resolvePool returns the per-app credential pool (production) or the dev
// pool (dev mode). See resolvePoolFor for the shared logic.
func (m *Module) resolvePool(ctx context.Context) (*pgxpool.Pool, func(), error) {
	return m.resolvePoolFor(ctx, db.CredentialFrom, db.CredentialProviderFrom)
}

// seedConn resolves an app-schema-scoped connection for system.SeedHandler
// (the dev-mount seed endpoint) WITHOUT the cross-module dev guard that
// Module.DB/Tx apply. The seed endpoint legitimately COPYs into another
// module's exposure-anchored dependency table — the platform already
// enforced that exposure-anchor grant before it ever sent the chunk (see
// devseed.Seeder.dependencyTables on the platform side) — so routing it
// through devGuardFor would reject every dependency-table seed in dev mode
// with a false-positive cross-module error.
//
// Also unlike Module.DB, this returns the concrete *pgxpool.Conn (via
// db.AcquireScopedConn) rather than the narrower db.Querier: COPY FROM STDIN
// needs the raw driver connection's PgConn(), which db.Querier does not
// expose.
func (m *Module) seedConn(ctx context.Context) (*pgxpool.Conn, func(), error) {
	pool, releasePool, err := m.resolvePool(ctx)
	if err != nil {
		return nil, nil, err
	}
	conn, releaseConn, err := db.AcquireScopedConn(ctx, pool)
	if err != nil {
		releasePool()
		return nil, nil, err
	}
	return conn, func() {
		releaseConn()
		releasePool()
	}, nil
}

// resolvePoolFor is the shared implementation behind resolvePool and
// resolveModulePool. Production reads a credential from the context via
// getCred (different context key per scope) and pulls a refcount-pinned
// pool from the cache. Dev mode falls through to the single dev pool, which
// is shared across all scopes — schema isolation in dev happens at the
// AcquireScoped layer via WithSchema, not at the pool level.
func (m *Module) resolvePoolFor(ctx context.Context, getCred func(context.Context) *db.Credential, getProvider func(context.Context) db.CredentialProvider) (*pgxpool.Pool, func(), error) {
	if provider := getProvider(ctx); provider != nil {
		return m.poolCache.GetProvider(ctx, provider)
	}
	if cred := getCred(ctx); cred != nil {
		return m.poolCache.Get(ctx, *cred)
	}
	if runtime.IsOneShot() {
		return nil, nil, fmt.Errorf("mirrorstack: database access in one-shot mode requires a renewable credential provider")
	}
	m.devDBOnce.Do(func() {
		m.devDB, m.devDBErr = db.Open(context.Background())
	})
	if m.devDBErr != nil {
		return nil, nil, m.devDBErr
	}
	return m.devDB.Pool(), func() {}, nil
}

// ModuleDB returns a connection scoped to this module's shared schema
// (mod_<id>). Use it for cross-app state — outbox tables, dedup ledgers,
// cross-app audit logs, rate limiters, module-wide config — anything a
// module needs that is owned by the module rather than by a single app.
//
// Production: uses the per-module credential injected by the platform via
// db.WithModuleCredential. Independent of the per-app credential read by
// Module.DB — a handler that needs both gets both, in the same context.
//
// Dev: shares the dev pool from DATABASE_URL with Module.DB; the schema is
// the only difference. The dev Postgres must have a mod_<id> schema for
// the queries to succeed.
//
// The module schema overlays any app schema set on the caller's context for
// this single call only — the caller's ctx is not mutated, so a subsequent
// Module.DB call sees the original app schema unchanged.
//
//	conn, release, err := mod.ModuleDB(r.Context())
//	if err != nil { ... }
//	defer release()
//	conn.Exec(ctx, "INSERT INTO outbox(...) VALUES(...)")
func (m *Module) ModuleDB(ctx context.Context) (db.Querier, func(), error) {
	pool, releasePool, err := m.resolveModulePool(ctx)
	if err != nil {
		return nil, nil, err
	}
	moduleCtx := db.WithSchema(ctx, m.moduleSchemaFor(ctx))
	querier, releaseConn, err := db.AcquireScoped(moduleCtx, pool)
	if err != nil {
		releasePool()
		return nil, nil, err
	}
	return m.devGuardFor(ctx, querier, db.ModuleCredentialFrom), func() {
		releaseConn()
		releasePool()
	}, nil
}

// ModuleTx runs fn inside a transaction scoped to the module's shared schema
// (mod_<id>). Commits on success, rolls back on error or panic. Use this for
// the outbox-pattern: insert the work record AND the dedup row in the same
// transaction so the worker only ever sees consistent state.
//
//	err := mod.ModuleTx(r.Context(), func(q db.Querier) error {
//	    queries := generated.New(q)
//	    if err := queries.InsertOutbox(ctx, ...); err != nil { return err }
//	    return queries.MarkProcessed(ctx, ...)
//	})
func (m *Module) ModuleTx(ctx context.Context, fn func(q db.Querier) error) error {
	pool, releasePool, err := m.resolveModulePool(ctx)
	if err != nil {
		return err
	}
	defer releasePool()
	moduleCtx := db.WithSchema(ctx, m.moduleSchemaFor(ctx))
	return db.Tx(moduleCtx, pool, func(q db.Querier) error {
		return fn(m.devGuardFor(ctx, q, db.ModuleCredentialFrom))
	})
}

// resolveModulePool reads the per-module credential instead of the per-app
// one. See resolvePoolFor for the shared logic.
func (m *Module) resolveModulePool(ctx context.Context) (*pgxpool.Pool, func(), error) {
	return m.resolvePoolFor(ctx, db.ModuleCredentialFrom, db.ModuleCredentialProviderFrom)
}

// moduleSchemaFor returns the Postgres schema (search_path target) for this
// module's shared cross-app state.
//
// In production, the platform's Lambda invoke shim resolves the live
// prefix from app_<app_id>.module_install.prefix and injects it via
// db.WithPrefix before invoking the module's handler. This lets the same
// compiled binary run against pre-Phase-2 installs (legacy "mod_<id>"
// form) and renamed Phase-2 installs uniformly — no compiled-SQL prefix.
//
// In dev mode (no platform shim) and for legacy modules with no
// module_install row, the helper falls back to "mod_" + Config.ID. The
// platform must pre-create this schema and grant the per-module DB role
// USAGE on it before any module handler runs.
func (m *Module) moduleSchemaFor(ctx context.Context) string {
	if p := db.PrefixFrom(ctx); p != "" {
		return p
	}
	return "mod_" + m.config.ID
}

// lifecycleTxRunner returns the TxRunner that should drive migrations for
// the given scope. Each scope runs against a different schema and uses a
// different DB credential, so the two scopes require different runners:
//
//   - ScopeApp → Module.Tx (reads db.CredentialFrom, per-app role, app_<id>
//     schema from ctx).
//   - ScopeModule → Module.ModuleTx (reads db.ModuleCredentialFrom, per-module
//     role, mod_<id> schema overlayed inside the transaction).
//
// Mixing these up is silently wrong: module migrations driven by the app
// credential would fail at Postgres because the per-(module, app) role lacks
// USAGE on mod_<id>, but that is infrastructure defense-in-depth — the SDK
// must pick the correct runner itself.
func (m *Module) lifecycleTxRunner(scope migration.Scope) migration.TxRunner {
	switch scope {
	case migration.ScopeModule:
		return m.ModuleTx
	case migration.ScopeApp:
		return m.Tx
	default:
		panic("mirrorstack: lifecycleTxRunner: unhandled scope " + string(scope))
	}
}

// lifecycleProvisioner returns the SDK-owned per-app setup the install/upgrade
// handlers run for the given scope, or nil when there is nothing to provision.
//
// Today that is the contributions store: <Config.ID>_contributions holds one
// app's registered contributions, so it lives in the APP schema and has to be
// created once per (app, module). The platform's install/upgrade call is the
// only deployed-plane window that carries both an app schema and a credential
// allowed to create tables in it — a cold-start hook could not do this, because
// one Lambda container serves many apps and would only ever provision the first
// one it happened to see. Module scope has no store of its own (mod_<id> is
// shared across apps, contributions are not), hence app scope only.
func (m *Module) lifecycleProvisioner(scope migration.Scope) system.Provisioner {
	if scope != migration.ScopeApp {
		return nil
	}
	return func(ctx context.Context) error {
		// No schema in the lifecycle body means the tunnel payload shape, where
		// the module runs against its own dev DB and the per-app store is
		// created by ensureDevAppSchema instead. Creating anything here would
		// land a per-app table in the connection's default schema — the wrong
		// place, and shared across every app.
		if db.SchemaFrom(ctx) == "" {
			return nil
		}
		return m.Tx(ctx, func(q db.Querier) error {
			// The contributions store is only ensured for a module that
			// declares slots. The count is read per CALL, never when the route
			// is mounted: ms.Provide runs after ms.Init, which is what mounts
			// the lifecycle routes, so a mount-time check would see zero slots
			// for every module that declares any — and silently provision
			// nothing, forever.
			if m.contribReg.Len() > 0 {
				if err := m.contribStorage.EnsureTable(ctx, q); err != nil {
					return err
				}
			}
			// The audit outbox is ensured for EVERY module, because any module
			// may record audit whether or not it hosts contribution slots.
			//
			// Go DDL rather than a migration, and it has to be: an SDK-owned
			// numbered migration can never reach an already-installed module —
			// it would sort below every stored watermark and be skipped
			// forever — and the SDK cannot ship .sql at all, because the module
			// owns the migration filesystem.
			return audit.EnsureTable(ctx, q)
		})
	}
}

// Package-level convenience wrappers — dispatch to defaultModule.

// DB returns a scoped database connection on the default module.
func DB(ctx context.Context) (db.Querier, func(), error) {
	return mustDefault("DB").DB(ctx)
}

// Tx runs fn inside a transaction on the default module.
func Tx(ctx context.Context, fn func(q db.Querier) error) error {
	return mustDefault("Tx").Tx(ctx, fn)
}

// ModuleDB returns a connection scoped to the module's shared schema on the
// default module.
func ModuleDB(ctx context.Context) (db.Querier, func(), error) {
	return mustDefault("ModuleDB").ModuleDB(ctx)
}

// ModuleTx runs fn inside a transaction scoped to the module's shared schema
// on the default module.
func ModuleTx(ctx context.Context, fn func(q db.Querier) error) error {
	return mustDefault("ModuleTx").ModuleTx(ctx, fn)
}
