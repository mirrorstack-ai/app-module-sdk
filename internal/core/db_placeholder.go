package core

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/mirrorstack-ai/app-module-sdk/db"
)

// This file closes the module-ID drift trap: a module's app-scope SQL
// (migrations under sql/app/*.sql, and sqlc-generated queries compiled from
// queries.sql) prefixes every table name with the module's catalog ID so it
// doesn't collide with another installed module's tables in the same shared
// app_<appID> schema. Historically that ID was baked in as literal text at
// scaffold time — if the module is later deleted and re-registered under a
// fresh catalog UUID, the runtime Config.ID no longer matches the ID baked
// into the SQL/Go source, and every query targets a table that was never
// created (or a migration creates tables under the wrong prefix entirely).
//
// The fix: module source (both raw .sql migrations and sqlc's queries.sql,
// which sqlc compiles verbatim into Go string literals) uses a fixed
// placeholder token, modulePlaceholderToken, instead of a real ID.
// placeholderQuerier substitutes the token for the module's actual runtime
// ID on every statement, right before it reaches the real connection —
// wrapping every app-scope db.Querier this package hands out (migrations
// via Module.Tx, and every sqlc-generated business query via Module.DB)
// gives the resolved SQL to the driver with no other code needing to know
// the placeholder exists. See withModuleID below for how this composes
// with devGuardFor's cross-module check.
//
// Module-scope (mod_<id>) tables are NOT affected: that ID is a schema name
// (mod_ + Config.ID), not a baked-in table-name prefix, so there is no
// literal ID text in module-scope SQL to substitute. ModuleDB/ModuleTx
// intentionally do not apply this wrapper.

// modulePlaceholderToken is the fixed marker module authors' SQL sources use
// in place of a real module ID. Chosen to be unambiguous inside SQL/Go
// string literals and to never collide with a real platform-minted ID
// (m<32 hex>), which contains no uppercase letters or underscores at the
// start.
const modulePlaceholderToken = "__MODULE_ID__"

// placeholderQuerier wraps a db.Querier and rewrites modulePlaceholderToken
// to the module's real runtime ID in every statement before delegating.
// Config.ID is validated at module construction time against
// moduleIDPattern (^[a-z][a-z0-9_]{0,35}$ — see module.go), which excludes
// quotes, whitespace, and every other SQL metacharacter, so the substituted
// value cannot break out of its position in the SQL text.
type placeholderQuerier struct {
	q  db.Querier
	id string
}

func (p placeholderQuerier) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return p.q.Exec(ctx, p.resolve(sql), args...)
}

func (p placeholderQuerier) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return p.q.Query(ctx, p.resolve(sql), args...)
}

func (p placeholderQuerier) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return p.q.QueryRow(ctx, p.resolve(sql), args...)
}

func (p placeholderQuerier) resolve(sql string) string {
	if !strings.Contains(sql, modulePlaceholderToken) {
		return sql
	}
	return strings.ReplaceAll(sql, modulePlaceholderToken, p.id)
}

// withModuleID wraps q so any occurrence of modulePlaceholderToken in a
// statement's SQL text resolves to this module's real runtime ID. Applied to
// every app-scope db.Querier this package hands out (Module.DB, Module.Tx),
// innermost relative to devGuardFor — call sites compose it as
// devGuardFor(ctx, withModuleID(q), ...), so devGuardFor's guardQuerier is
// actually the OUTER layer and runs FIRST: it scans the raw statement text,
// still containing the literal modulePlaceholderToken, before delegating to
// this wrapper, which resolves the token last, right before the statement
// reaches the real connection. That order is safe even though the guard
// never sees a resolved own-table reference: the guard's cross-module
// regexes (moduleTableRe, moduleSchemaRe) only match the platform-minted
// m<32 hex> form, which modulePlaceholderToken can never satisfy (it has
// uppercase letters and underscores outside the hex alphabet), so an
// unresolved own-table reference is simply invisible to the guard rather
// than misclassified — never a false accept or a false reject. A foreign
// module's table reference is unaffected by any of this: it is always
// literal real-ID text baked into the calling module's own SQL, not
// something this wrapper ever touches, so the guard still catches it
// exactly as before.
func (m *Module) withModuleID(q db.Querier) db.Querier {
	return placeholderQuerier{q: q, id: m.config.ID}
}
