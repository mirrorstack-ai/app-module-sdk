package core

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/migration"
)

// DB returns a scoped database connection.
//
// Production: uses per-app credentials injected by the platform via Lambda payload.
// Dev: uses DATABASE_URL env var with localhost fallback.
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
	return querier, func() {
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
	return db.Tx(ctx, pool, fn)
}

// resolvePool returns the per-app credential pool (production) or the dev
// pool (dev mode). See resolvePoolFor for the shared logic.
func (m *Module) resolvePool(ctx context.Context) (*pgxpool.Pool, func(), error) {
	return m.resolvePoolFor(ctx, db.CredentialFrom)
}

// resolvePoolFor is the shared implementation behind resolvePool and
// resolveModulePool. Production reads a credential from the context via
// getCred (different context key per scope) and pulls a refcount-pinned
// pool from the cache. Dev mode falls through to the single dev pool, which
// is shared across all scopes — schema isolation in dev happens at the
// AcquireScoped layer via WithSchema, not at the pool level.
func (m *Module) resolvePoolFor(ctx context.Context, getCred func(context.Context) *db.Credential) (*pgxpool.Pool, func(), error) {
	if cred := getCred(ctx); cred != nil {
		return m.poolCache.Get(ctx, *cred)
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
	moduleCtx := db.WithSchema(ctx, m.moduleSchema())
	querier, releaseConn, err := db.AcquireScoped(moduleCtx, pool)
	if err != nil {
		releasePool()
		return nil, nil, err
	}
	return querier, func() {
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
	moduleCtx := db.WithSchema(ctx, m.moduleSchema())
	return db.Tx(moduleCtx, pool, fn)
}

// resolveModulePool reads the per-module credential instead of the per-app
// one. See resolvePoolFor for the shared logic.
func (m *Module) resolveModulePool(ctx context.Context) (*pgxpool.Pool, func(), error) {
	return m.resolvePoolFor(ctx, db.ModuleCredentialFrom)
}

// moduleSchema returns the Postgres schema name for this module's shared
// state. Convention: "mod_" + the developer's Config.ID. The platform must
// pre-create this schema and grant the per-module DB role USAGE on it
// before any module handler runs.
func (m *Module) moduleSchema() string {
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
