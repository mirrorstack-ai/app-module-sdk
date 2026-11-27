package sdktest

import (
	"context"
	"fmt"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/db"
)

// sdkOwnedTables are tables the SDK itself creates in every app schema under a
// fixed name; they are shared infrastructure, not module tables.
var sdkOwnedTables = []string{"schema_migrations"}

// TablesMissingPrefix returns the base tables in schema whose name does not
// start with "<moduleID>_", sorted by name. SDK-owned tables (schema_migrations)
// are excluded. Run it after the module's migrations have been applied.
func TablesMissingPrefix(ctx context.Context, q db.Querier, moduleID, schema string) ([]string, error) {
	// left()/length() instead of LIKE so the "_" in the prefix is not a
	// single-character wildcard.
	rows, err := q.Query(ctx, `
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = $1
		  AND table_type = 'BASE TABLE'
		  AND left(table_name, length($2::text)) <> $2::text
		  AND table_name <> ALL($3::text[])
		ORDER BY table_name`, schema, moduleID+"_", sdkOwnedTables)
	if err != nil {
		return nil, fmt.Errorf("sdktest: list tables in %q: %w", schema, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("sdktest: scan table name: %w", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sdktest: list tables in %q: %w", schema, err)
	}
	return out, nil
}

// ValidateTablePrefix fails t when any table in schema does not start with
// "<moduleID>_". Every table a module creates in its shared app_<id> schema must
// carry that prefix so two modules cannot collide on a name like "items".
//
// It is an integration-test helper: q must be a live connection (a *pgxpool.Pool
// works) pointing at a database where the module's migrations have already
// been applied into schema. Callers skip the test themselves when no database
// is configured:
//
//	func TestTablePrefix(t *testing.T) {
//	    pool := connectOrSkip(t)
//	    sdktest.ValidateTablePrefix(t, context.Background(), pool, "mymod", "app_xxx")
//	}
func ValidateTablePrefix(t testing.TB, ctx context.Context, q db.Querier, moduleID, schema string) {
	t.Helper()
	bad, err := TablesMissingPrefix(ctx, q, moduleID, schema)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(bad) > 0 {
		t.Errorf("tables in schema %q must start with %q; offending: %v", schema, moduleID+"_", bad)
	}
}
