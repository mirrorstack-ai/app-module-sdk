//go:build integration

package system

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/db"
)

// seedTestDB opens the dev pool for the seed integration tests, skipping
// (not failing) when no Postgres is reachable — same convention as
// db.testDB / core's dependency_db_deployed_integration_test.go.
func seedTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(context.Background())
	if err != nil {
		t.Skipf("skipping: cannot connect to postgres: %v", err)
	}
	t.Cleanup(d.Close)
	return d
}

// seedAcquirerFor wires a SeedConnAcquirer straight to db.AcquireScopedConn —
// the same acquire path Module.seedConn uses in internal/core/db.go, minus
// the pool-cache/dev-guard machinery that package doesn't need to prove
// here. SeedHandler itself is responsible for putting the app schema on ctx
// before calling this.
func seedAcquirerFor(d *db.DB) SeedConnAcquirer {
	return func(ctx context.Context) (SeedConn, func(), error) {
		return db.AcquireScopedConn(ctx, d.Pool())
	}
}

// doSeed POSTs req through SeedHandler and decodes the response.
func doSeed(t *testing.T, h http.HandlerFunc, req SeedRequest) (int, SeedResponse) {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/seed", strings.NewReader(string(body))))

	var resp SeedResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
		}
	}
	return rec.Code, resp
}

func TestSeedHandler_Integration_FirstChunkIntoEmptyTable(t *testing.T) {
	d := seedTestDB(t)
	ctx := context.Background()

	const appID = "seedtestempty"
	const schema = "app_seedtestempty"
	mustExecAdmin(t, d, ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	mustExecAdmin(t, d, ctx, "CREATE SCHEMA "+schema)
	t.Cleanup(func() { d.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE") })
	// Already-migrated own table: pre-existing and empty, the common case for
	// a freshly dev-provisioned app.
	mustExecAdmin(t, d, ctx, "CREATE TABLE "+schema+".items (id int, title text)")

	h := SeedHandler(seedAcquirerFor(d))
	code, resp := doSeed(t, h, SeedRequest{
		AppID:   appID,
		Table:   "items",
		Columns: []string{"id", "title"},
		Data:    "1\tfoo\n2\tbar\n",
		First:   true,
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if resp.Skipped {
		t.Fatalf("Skipped = true, want false (table was empty)")
	}

	rows := queryTitles(t, d, ctx, schema, "items")
	want := map[string]bool{"foo": true, "bar": true}
	if len(rows) != 2 || !want[rows[0]] || !want[rows[1]] {
		t.Errorf("titles = %v, want exactly [foo bar] in some order", rows)
	}
}

func TestSeedHandler_Integration_FirstChunkIntoNonEmptyTable(t *testing.T) {
	d := seedTestDB(t)
	ctx := context.Background()

	const appID = "seedtestnonempty"
	const schema = "app_seedtestnonempty"
	mustExecAdmin(t, d, ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	mustExecAdmin(t, d, ctx, "CREATE SCHEMA "+schema)
	t.Cleanup(func() { d.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE") })
	mustExecAdmin(t, d, ctx, "CREATE TABLE "+schema+".items (id int, title text)")
	// The developer's own local write — must survive the seed untouched.
	mustExecAdmin(t, d, ctx, "INSERT INTO "+schema+".items (id, title) VALUES (99, 'developer-local')")

	h := SeedHandler(seedAcquirerFor(d))
	code, resp := doSeed(t, h, SeedRequest{
		AppID:   appID,
		Table:   "items",
		Columns: []string{"id", "title"},
		Data:    "1\tfoo\n",
		First:   true,
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if !resp.Skipped {
		t.Fatalf("Skipped = false, want true (table already had a row)")
	}

	rows := queryTitles(t, d, ctx, schema, "items")
	if len(rows) != 1 || rows[0] != "developer-local" {
		t.Errorf("titles = %v, want exactly [developer-local] — seed must not touch a non-empty table", rows)
	}
}

func TestSeedHandler_Integration_CreateSQLDependencyTable(t *testing.T) {
	d := seedTestDB(t)
	ctx := context.Background()

	const appID = "seedtestdep"
	const schema = "app_seedtestdep"
	mustExecAdmin(t, d, ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	mustExecAdmin(t, d, ctx, "CREATE SCHEMA "+schema)
	t.Cleanup(func() { d.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE") })
	// Deliberately no pre-existing table: this is the dependency-table case —
	// the dev DB never migrated a producer module it doesn't own.

	h := SeedHandler(seedAcquirerFor(d))
	code, resp := doSeed(t, h, SeedRequest{
		AppID:     appID,
		Table:     "m81b3ac7081c1409495700c761e23b59e_categories",
		Columns:   []string{"id", "title"},
		CreateSQL: `CREATE TABLE IF NOT EXISTS "m81b3ac7081c1409495700c761e23b59e_categories" (id int, title text)`,
		Data:      "1\tproducer-row\n",
		First:     true,
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body should carry {skipped:false}", code)
	}
	if resp.Skipped {
		t.Fatalf("Skipped = true, want false (dependency table was freshly created and empty)")
	}

	rows := queryTitles(t, d, ctx, schema, `"m81b3ac7081c1409495700c761e23b59e_categories"`)
	if len(rows) != 1 || rows[0] != "producer-row" {
		t.Errorf("titles = %v, want exactly [producer-row]", rows)
	}
}

func TestSeedHandler_Integration_CreateSQLOnlyEmptyDependencyTable(t *testing.T) {
	d := seedTestDB(t)
	ctx := context.Background()

	const appID = "seedtestdepempty"
	const schema = "app_seedtestdepempty"
	mustExecAdmin(t, d, ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	mustExecAdmin(t, d, ctx, "CREATE SCHEMA "+schema)
	t.Cleanup(func() { d.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE") })

	// A producer's dependency table with zero live rows still needs the
	// CREATE materialized locally — the seeder sends CreateSQL with no Data
	// (see devseed.Seeder.seedTable's tail case on the platform side).
	h := SeedHandler(seedAcquirerFor(d))
	code, resp := doSeed(t, h, SeedRequest{
		AppID:     appID,
		Table:     "m81b3ac7081c1409495700c761e23b59e_empty_dep",
		Columns:   []string{"id"},
		CreateSQL: `CREATE TABLE IF NOT EXISTS "m81b3ac7081c1409495700c761e23b59e_empty_dep" (id int)`,
		Data:      "",
		First:     true,
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if resp.Skipped {
		t.Fatalf("Skipped = true, want false")
	}

	var exists bool
	if err := d.Pool().QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = $1 AND table_name = $2)`,
		schema, "m81b3ac7081c1409495700c761e23b59e_empty_dep").Scan(&exists); err != nil {
		t.Fatalf("check table exists: %v", err)
	}
	if !exists {
		t.Error("dependency table was not created")
	}
}

func TestSeedHandler_Integration_ContinuationChunkAppends(t *testing.T) {
	d := seedTestDB(t)
	ctx := context.Background()

	const appID = "seedtestcontinuation"
	const schema = "app_seedtestcontinuation"
	mustExecAdmin(t, d, ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	mustExecAdmin(t, d, ctx, "CREATE SCHEMA "+schema)
	t.Cleanup(func() { d.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE") })
	mustExecAdmin(t, d, ctx, "CREATE TABLE "+schema+".items (id int, title text)")

	h := SeedHandler(seedAcquirerFor(d))
	code, resp := doSeed(t, h, SeedRequest{
		AppID: appID, Table: "items", Columns: []string{"id", "title"},
		Data: "1\tfoo\n", First: true,
	})
	if code != http.StatusOK || resp.Skipped {
		t.Fatalf("first chunk: status=%d skipped=%v, want 200/false", code, resp.Skipped)
	}
	// Continuation chunk: First=false must skip the if-empty guard (the table
	// now has a row from the chunk above) and append rather than replace.
	code, resp = doSeed(t, h, SeedRequest{
		AppID: appID, Table: "items", Columns: []string{"id", "title"},
		Data: "2\tbar\n", First: false,
	})
	if code != http.StatusOK || resp.Skipped {
		t.Fatalf("continuation chunk: status=%d skipped=%v, want 200/false", code, resp.Skipped)
	}

	rows := queryTitles(t, d, ctx, schema, "items")
	if len(rows) != 2 {
		t.Errorf("titles = %v, want 2 rows (foo appended with bar, not replaced)", rows)
	}
}

// mustExecAdmin runs sql on the unscoped dev pool (no app schema on ctx) —
// used for test fixture setup/teardown, which operates on schema-qualified
// names directly rather than through the app-schema search_path SeedHandler
// itself relies on.
func mustExecAdmin(t *testing.T, d *db.DB, ctx context.Context, sql string) {
	t.Helper()
	if _, err := d.Exec(ctx, sql); err != nil {
		t.Fatalf("mustExecAdmin %q: %v", sql, err)
	}
}

// queryTitles reads every "title"-ish value back out of schema.table in
// insertion order, for asserting COPY landed the expected rows. table may
// already be identifier-quoted by the caller (for m<hex>_ names that need
// exact-case quoting); schema is always plain and gets quoted here.
func queryTitles(t *testing.T, d *db.DB, ctx context.Context, schema, table string) []string {
	t.Helper()
	rows, err := d.Pool().Query(ctx, `SELECT title FROM `+schema+"."+table)
	if err != nil {
		t.Fatalf("query %s.%s: %v", schema, table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var title string
		if err := rows.Scan(&title); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, title)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}
