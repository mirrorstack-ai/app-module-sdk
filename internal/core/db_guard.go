package core

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/mirrorstack-ai/app-module-sdk/db"
)

// This file closes the "works on my machine" trap (decision 17 §2): in dev
// mode every module shares one owner pool with search_path-only scoping, so a
// raw SQL read/JOIN against ANOTHER module's tables silently succeeds locally
// — but can never work deployed, where the per-(app,module) Postgres role
// holds no grant on foreign tables. The guard makes dev fail the same way
// prod does, at the SDK's query chokepoint.
//
// KNOWN LIMITS (this is an honest dev-mode fail-fast, not a security
// boundary — production enforcement is the DB role's grants):
//   - indirection through views or functions that touch a foreign table is
//     not caught (the foreign name never appears in the module's SQL);
//   - a foreign table reached only via an unprefixed name (no m<hex>_
//     segment anywhere in the statement) is not caught;
//   - direct use of the low-level db.Open()/db.DB client bypasses Module.DB
//     and therefore the guard.

// errCrossModuleRelation is the sentinel wrapped into every guard rejection,
// so tests (and callers inside this package) can errors.Is the failure mode.
var errCrossModuleRelation = errors.New("cross-module table access")

// moduleTableRe finds physical module-table identifiers: the platform-minted
// module ID form m<32 hex> followed by an underscore and the table name
// (m<hex>_users, app_<id>.m<hex>_users, "m<hex>_users"). The leading
// (^|[^a-z0-9]) guard stops mid-word matches (e.g. inside a longer hex blob)
// while still matching after '.', '"', '(' and '_'. Case-folded because
// Postgres lowercases unquoted identifiers.
var moduleTableRe = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])((m[0-9a-f]{32})_[a-z0-9_]*)`)

// moduleSchemaRe finds references to a module's cross-app schema
// (mod_m<hex>.some_table), which end at the hex id — no trailing underscore —
// so moduleTableRe cannot see them.
var moduleSchemaRe = regexp.MustCompile(`(?i)(?:^|[^a-z0-9_])(mod_(m[0-9a-f]{32}))(?:[^a-z0-9_]|$)`)

// crossModuleRes is hoisted to package level because the guard runs on every
// dev-mode statement — no per-query slice allocation.
var crossModuleRes = [...]*regexp.Regexp{moduleTableRe, moduleSchemaRe}

// checkCrossModuleSQL scans one SQL statement for physical table or schema
// names owned by a different module and rejects with a clear, actionable
// error. String literals and comments are stripped first so mentioning a
// foreign table in a logged message or comment does not false-positive.
// Only platform-minted m<32 hex> IDs are recognized — an unregistered dev
// module's ad-hoc ID has no deployed physical form to protect.
func checkCrossModuleSQL(ownID, sql string) error {
	stripped := stripSQLNoise(sql)
	for _, re := range crossModuleRes {
		for _, match := range re.FindAllStringSubmatch(stripped, -1) {
			if id := strings.ToLower(match[2]); id != ownID {
				return fmt.Errorf(
					"mirrorstack/db: query references %q, which belongs to another module (module %q): %w — "+
						"this only appears to work in local dev because dev shares one database; "+
						"a deployed module's DB role has no grant on foreign tables, so it will always fail in production. "+
						"Declare the table with ms.DependsOn + n.Table(...) and read it through ms.DependencyDB instead",
					strings.ToLower(match[1]), id, errCrossModuleRelation)
			}
		}
	}
	return nil
}

// stripSQLNoise removes the regions of a SQL statement where an identifier
// pattern is meaningless: single-quoted string literals (with doubled-quote
// escapes), line comments, nested block comments, and dollar-quoted strings. Each
// removed region is replaced by a single space so identifier boundaries on
// either side stay intact. Double-quoted regions are kept — those are
// identifiers, exactly what the guard inspects.
func stripSQLNoise(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == '\'':
			i = skipSingleQuoted(s, i)
			b.WriteByte(' ')
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			i = skipBlockComment(s, i)
			b.WriteByte(' ')
		case c == '$':
			if end, ok := skipDollarQuoted(s, i); ok {
				i = end
				b.WriteByte(' ')
				continue
			}
			b.WriteByte(c)
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// skipSingleQuoted returns the index just past a '...' literal starting at
// s[start] (a doubled single quote inside the literal escapes a quote). An
// unterminated literal consumes the rest of the string — invalid SQL Postgres
// would reject anyway.
func skipSingleQuoted(s string, start int) int {
	i := start + 1
	for i < len(s) {
		if s[i] == '\'' {
			if i+1 < len(s) && s[i+1] == '\'' {
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return i
}

// skipBlockComment returns the index just past a /* ... */ comment starting
// at s[start]. Postgres block comments nest, so depth is tracked.
func skipBlockComment(s string, start int) int {
	depth := 1
	i := start + 2
	for i < len(s) && depth > 0 {
		switch {
		case s[i] == '/' && i+1 < len(s) && s[i+1] == '*':
			depth++
			i += 2
		case s[i] == '*' && i+1 < len(s) && s[i+1] == '/':
			depth--
			i += 2
		default:
			i++
		}
	}
	return i
}

// skipDollarQuoted checks for a dollar-quoted string ($$...$$ or
// $tag$...$tag$) starting at s[start] and returns the index just past it.
// ok=false when s[start] starts a parameter placeholder ($1, $2, ...) or any
// other non-dollar-quote use of '$'.
func skipDollarQuoted(s string, start int) (end int, ok bool) {
	i := start + 1
	if i < len(s) && isDollarTagStart(s[i]) {
		i++
		for i < len(s) && isDollarTagChar(s[i]) {
			i++
		}
	}
	if i >= len(s) || s[i] != '$' {
		return 0, false
	}
	tag := s[start : i+1] // "$tag$" including both dollars
	rest := strings.Index(s[i+1:], tag)
	if rest < 0 {
		return len(s), true // unterminated — consume the rest
	}
	return i + 1 + rest + len(tag), true
}

func isDollarTagStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isDollarTagChar(c byte) bool {
	return isDollarTagStart(c) || (c >= '0' && c <= '9')
}

// guardQuerier wraps a db.Querier and runs checkCrossModuleSQL before every
// statement. Applied to dev-pool connections only — production connections
// are enforced by Postgres itself (the per-(app,module) role's grants).
type guardQuerier struct {
	q     db.Querier
	ownID string
}

func (g guardQuerier) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if err := checkCrossModuleSQL(g.ownID, sql); err != nil {
		return pgconn.CommandTag{}, err
	}
	return g.q.Exec(ctx, sql, args...)
}

func (g guardQuerier) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if err := checkCrossModuleSQL(g.ownID, sql); err != nil {
		return nil, err
	}
	return g.q.Query(ctx, sql, args...)
}

func (g guardQuerier) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if err := checkCrossModuleSQL(g.ownID, sql); err != nil {
		return errRow{err: err}
	}
	return g.q.QueryRow(ctx, sql, args...)
}

// errRow satisfies pgx.Row for the QueryRow signature, which has no error
// return of its own — the guard rejection surfaces on Scan, same as pgx's
// own deferred-error rows.
type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

// devGuardFor wraps q with the cross-module fail-fast guard when the
// invocation runs against the shared dev pool — detected exactly the way
// resolvePoolFor picks the pool: no platform credential in ctx. getCred is
// db.CredentialFrom for the app scope and db.ModuleCredentialFrom for the
// module scope, matching the credential the corresponding resolve used.
func (m *Module) devGuardFor(ctx context.Context, q db.Querier, getCred func(context.Context) *db.Credential) db.Querier {
	if getCred(ctx) != nil {
		return q
	}
	return guardQuerier{q: q, ownID: m.config.ID}
}
