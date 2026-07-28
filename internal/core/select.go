package core

// THE DEPLOYED-PLANE DYNAMIC-SELECT INTERPOLATION POINT (SDK side).
//
// This is a FAITHFUL PORT of api-platform's blessed dynamic SELECT
// (internal/shared/database/select.go — QueryDynamicSelect / DynamicSelect /
// buildDynamicSelect). The deployed cross-module read (decision 18 §3) needs a
// statement whose identifiers are only known at request time — a per-producer
// physical relation name (m<hex>_<table>, supplied by the platform manifest, not
// derived here), a caller-chosen projection, and per-request filter columns.
// The SDK CANNOT import api-platform, so the one dynamic SELECT it is allowed to
// compose is ported here where it can be reviewed as a unit. The golden test in
// select_test.go locks (SQL text + args) byte-for-byte against the api-platform
// source so the two cannot drift.
//
// Injection is impossible by construction, on three independent layers, exactly
// as in the source:
//
//  1. relation-name provenance — the physical Table name is never module input.
//     On the DEPLOYED plane it is PLATFORM-OWNED: it comes only from the trusted
//     injected manifest (DependencyGrant.Tables). On a CO-LOCATED DEV read there
//     is no platform to supply it, so it is SDK-DERIVED by localPhysicalName
//     (dependency_local.go) from a moduleIDPattern-validated producer id joined
//     to a dependencySQLName-validated logical table name — neither of which can
//     contain a metacharacter. Columns/filter columns are logical names the
//     consumer declared, on both planes. Layers 2 and 3 below are unchanged and
//     still make injection impossible in either case.
//  2. name-shape gate (ENFORCED HERE) — every identifier must match
//     selectIdentPattern (lowercase snake_case within Postgres's 63-byte budget,
//     the only shape ExposeTable / the migration DSL can produce). Quotes,
//     semicolons, dots, uppercase, spaces, overlong names all fail with
//     errUnsafeSelectIdentifier before any SQL text is built.
//  3. identifier quoting (ENFORCED HERE) — every identifier still goes through
//     pgx.Identifier.Sanitize() (double-quote + escape).
//
// Filter VALUES are never interpolated — always bound as $n positional
// parameters. The only non-identifier text is the LIMIT, a clamped int owned
// here.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

const (
	// defaultDynamicSelectLimit is applied when the caller sends no limit (or a
	// non-positive one). Mirrors api-platform DefaultDynamicSelectLimit.
	defaultDynamicSelectLimit = 200
	// maxDynamicSelectLimit is the hard row ceiling per dynamic read — a
	// fetch-then-join-in-app-code surface, not a bulk-export channel. Mirrors
	// api-platform MaxDynamicSelectLimit.
	maxDynamicSelectLimit = 2000
)

// errUnsafeSelectIdentifier reports an identifier that failed the name-shape
// gate. Reaching it means an upstream validation layer was bypassed.
var errUnsafeSelectIdentifier = errors.New("mirrorstack: unsafe dynamic identifier")

// selectIdentPattern is the only identifier shape a dynamic SELECT accepts:
// lowercase snake_case, starting with a letter, within Postgres's 63-byte
// budget. Byte-identical to api-platform's safeIdentifierPattern and to
// dependencySQLName; a physical relation name (m<hex>_<table>) also matches it.
var selectIdentPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// SelectFilter is one WHERE predicate: Column = value (one Values entry) or
// Column IN (...) (several). Values are always bound as $n parameters.
type SelectFilter struct {
	Column string
	Values []any
}

// DynamicSelect describes the one dynamic statement shape composed here: a
// projection of whitelisted columns from one physical relation, ANDed
// equality/IN filters, and a row limit. The caller NEVER supplies SQL.
type DynamicSelect struct {
	Schema  string
	Table   string // physical relation name from the manifest, e.g. m<hex>_<table>
	Columns []string
	Filters []SelectFilter
	Limit   int // <=0 → defaultDynamicSelectLimit; clamped to maxDynamicSelectLimit
}

// queryDynamicSelect composes and executes q inside the caller-supplied READ
// ONLY transaction and returns the rows as column→value maps plus whether the
// read was cut at the limit. Pair it with db.TxReadOnly so Postgres itself
// rejects any write on the same tx (SQLSTATE 25006). pgx.RowToMap decodes
// numeric columns to their native Go types (int64 for bigint) so join keys keep
// full fidelity — the consumer's stringField reads them via its default branch.
func queryDynamicSelect(ctx context.Context, tx pgx.Tx, q DynamicSelect) (_ []map[string]any, truncated bool, _ error) {
	sql, args, limit, err := buildDynamicSelect(q)
	if err != nil {
		return nil, false, err
	}

	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, false, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToMap) // closes rows; non-nil even when empty
	if err != nil {
		return nil, false, err
	}

	// The SELECT asked for limit+1 rows; an overflow row means truncation.
	if len(out) > limit {
		out = out[:limit]
		truncated = true
	}
	return out, truncated, nil
}

// buildDynamicSelect composes the parameterized SELECT text. Every identifier
// is shape-gated then Sanitize()d; every filter value becomes a $n parameter.
// Returns the effective (clamped) limit — the statement asks for limit+1 rows
// so the executor can report truncation without a second COUNT query. This is
// the byte-for-byte port the golden test locks against api-platform.
func buildDynamicSelect(q DynamicSelect) (sql string, args []any, limit int, err error) {
	if err := requireSafeSelectIdentifiers(q); err != nil {
		return "", nil, 0, err
	}
	if len(q.Columns) == 0 {
		return "", nil, 0, errors.New("mirrorstack: dynamic select requires at least one column")
	}

	var sb strings.Builder
	sb.WriteString("SELECT ")
	for i, c := range q.Columns {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(pgx.Identifier{c}.Sanitize())
	}
	sb.WriteString(" FROM ")
	sb.WriteString(pgx.Identifier{q.Schema, q.Table}.Sanitize())

	for i, f := range q.Filters {
		if len(f.Values) == 0 {
			return "", nil, 0, fmt.Errorf("mirrorstack: dynamic select filter %q has no values", f.Column)
		}
		if i == 0 {
			sb.WriteString(" WHERE ")
		} else {
			sb.WriteString(" AND ")
		}
		ident := pgx.Identifier{f.Column}.Sanitize()
		if len(f.Values) == 1 {
			args = append(args, f.Values[0])
			fmt.Fprintf(&sb, "%s = $%d", ident, len(args))
			continue
		}
		placeholders := make([]string, len(f.Values))
		for j, v := range f.Values {
			args = append(args, v)
			placeholders[j] = fmt.Sprintf("$%d", len(args))
		}
		fmt.Fprintf(&sb, "%s IN (%s)", ident, strings.Join(placeholders, ", "))
	}

	limit = clampDynamicSelectLimit(q.Limit)
	fmt.Fprintf(&sb, " LIMIT %d", limit+1)
	return sb.String(), args, limit, nil
}

// requireSafeSelectIdentifiers runs the name-shape gate over every identifier.
func requireSafeSelectIdentifiers(q DynamicSelect) error {
	idents := []string{q.Schema, q.Table}
	idents = append(idents, q.Columns...)
	for _, f := range q.Filters {
		idents = append(idents, f.Column)
	}
	for _, id := range idents {
		if !selectIdentPattern.MatchString(id) {
			return fmt.Errorf("%w: %q", errUnsafeSelectIdentifier, id)
		}
	}
	return nil
}

// clampDynamicSelectLimit normalizes the caller's limit: <=0 → the default,
// above the ceiling → the ceiling.
func clampDynamicSelectLimit(n int) int {
	switch {
	case n <= 0:
		return defaultDynamicSelectLimit
	case n > maxDynamicSelectLimit:
		return maxDynamicSelectLimit
	default:
		return n
	}
}
