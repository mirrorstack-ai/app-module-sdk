package core

import (
	"context"
	"fmt"
	"os"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
)

// driftTestDBURL returns the dev module-Postgres URL these integration tests
// connect to. MS_LOCAL_DB_URL wins (the var `mirrorstack dev` exports); the
// fallback matches the live ms-app-modules compose Postgres reachable over the
// OrbStack container network. Tests skip cleanly when neither is reachable.
func driftTestDBURL() string {
	if u := os.Getenv("MS_LOCAL_DB_URL"); u != "" {
		return u
	}
	return "postgres://mirrorstack:mirrorstack@192.168.107.3:5432/ms_app_modules?sslmode=disable"
}

func qualifiedTables(t *testing.T, pool *pgx.Conn, schema, prefix string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM information_schema.tables
		 WHERE table_schema=$1 AND table_type='BASE TABLE' AND table_name LIKE $2`,
		schema, prefix+"\\_%").Scan(&n); err != nil {
		t.Fatalf("count tables %s.%s_*: %v", schema, prefix, err)
	}
	return n
}

func qualifiedTableExists(t *testing.T, pool *pgx.Conn, schema, table string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(context.Background(),
		`SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema=$1 AND table_type='BASE TABLE' AND table_name=$2
		)`, schema, table).Scan(&exists); err != nil {
		t.Fatalf("look up table %s.%s: %v", schema, table, err)
	}
	return exists
}

// TestDevAppSchema_TwoModulesShareSchema_Integration is the regression guard for
// the cross-module tracking collision: two modules provisioning the SAME app
// schema must each get their own tables. Before the per-module tracking key,
// the second module saw the first module's (scope,version) rows, decided its
// migrations were "already applied", and skipped them — leaving its tables
// missing while a tracking row claimed success.
func TestDevAppSchema_TwoModulesShareSchema_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	resetDefault(t)
	t.Setenv(devMigrateEnvVar, driftTestDBURL())

	coreSQL := fstest.MapFS{
		"sql/app/0001_init.up.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE IF NOT EXISTS mcore_items (id text PRIMARY KEY)`),
		},
	}
	googSQL := fstest.MapFS{
		"sql/app/0001_init.up.sql": &fstest.MapFile{
			// Same version "0001" as mcore on purpose — proves the keys don't collide.
			Data: []byte(`CREATE TABLE IF NOT EXISTS mgoog_nonces (nonce text PRIMARY KEY)`),
		},
	}

	mcore, err := New(Config{ID: "mcore", Name: "Core", SQL: coreSQL})
	if err != nil {
		t.Fatalf("New mcore: %v", err)
	}
	mgoog, err := New(Config{ID: "mgoog", Name: "Goog", SQL: googSQL})
	if err != nil {
		t.Fatalf("New mgoog: %v", err)
	}

	ctx := context.Background()
	pool, release, err := mcore.resolvePool(ctx)
	if err != nil {
		t.Skipf("skipping (no dev postgres): %v", err)
	}
	defer release()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("skipping (no dev postgres): %v", err)
	}

	schema, _ := devAppSchemaName("aaaaaaaa-0000-0000-0000-000000000001")
	ident := func(s string) string { return pgx.Identifier{s}.Sanitize() }
	cleanup := func() {
		pool.Exec(ctx, `DROP SCHEMA IF EXISTS `+ident(schema)+` CASCADE`)
		pool.Exec(ctx, `DROP TABLE IF EXISTS public.mcore_items`)
		pool.Exec(ctx, `DROP TABLE IF EXISTS public.mgoog_nonces`)
	}
	cleanup()
	t.Cleanup(cleanup)

	if err := mcore.ensureDevAppSchema(ctx, schema); err != nil {
		t.Fatalf("provision mcore: %v", err)
	}
	if err := mgoog.ensureDevAppSchema(ctx, schema); err != nil {
		t.Fatalf("provision mgoog: %v", err)
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()
	raw := conn.Conn()

	if !qualifiedTableExists(t, raw, schema, "mcore_items") {
		t.Errorf("mcore_items is missing from %s", schema)
	}
	if !qualifiedTableExists(t, raw, schema, "mgoog_nonces") {
		t.Errorf("mgoog_nonces is missing from %s — cross-module tracking collision", schema)
	}
	// Neither module's DDL leaked into public.
	if got := qualifiedTables(t, raw, "public", "mgoog"); got != 0 {
		t.Errorf("mgoog tables leaked into public = %d, want 0 — search_path not pinned", got)
	}
}

// TestDevAppSchema_DriftSelfHeal_Integration is the regression guard for the
// search_path drift bug: a migration recorded as applied but whose table is
// missing from the app schema (it leaked into public) must be re-applied on the
// next dev run.
func TestDevAppSchema_DriftSelfHeal_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	resetDefault(t)
	t.Setenv(devMigrateEnvVar, driftTestDBURL())

	sqlFS := fstest.MapFS{
		"sql/app/0001_init.up.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE IF NOT EXISTS mheal_items (id text PRIMARY KEY)`),
		},
	}
	m, err := New(Config{ID: "mheal", Name: "Heal", SQL: sqlFS})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	pool, release, err := m.resolvePool(ctx)
	if err != nil {
		t.Skipf("skipping (no dev postgres): %v", err)
	}
	defer release()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("skipping (no dev postgres): %v", err)
	}

	schema, _ := devAppSchemaName("bbbbbbbb-0000-0000-0000-000000000002")
	ident := func(s string) string { return pgx.Identifier{s}.Sanitize() }
	cleanup := func() { pool.Exec(ctx, `DROP SCHEMA IF EXISTS `+ident(schema)+` CASCADE`) }
	cleanup()
	t.Cleanup(cleanup)

	if err := m.ensureDevAppSchema(ctx, schema); err != nil {
		t.Fatalf("provision: %v", err)
	}

	// Simulate the drift: the migration is recorded, but its table is gone from
	// the schema (the historical bug landed it in public instead).
	if _, err := pool.Exec(ctx, fmt.Sprintf(`DROP TABLE %s.mheal_items`, ident(schema))); err != nil {
		t.Fatalf("simulate drift drop: %v", err)
	}
	// Tracking row still claims 0001 is applied.
	var rows int
	pool.QueryRow(ctx, fmt.Sprintf(
		`SELECT count(*) FROM %s.schema_migrations WHERE module_id='mheal' AND scope='app' AND version='0001'`,
		ident(schema))).Scan(&rows)
	if rows != 1 {
		t.Fatalf("expected tracking row pre-drop, got %d", rows)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire before self-heal: %v", err)
	}
	if !qualifiedTableExists(t, conn.Conn(), schema, "mheal_audit_outbox") {
		conn.Release()
		t.Fatal("audit outbox is missing before self-heal — test cannot prove SDK stores are excluded from drift evidence")
	}
	conn.Release()

	// Re-provision must detect drift and re-apply (the sync.Once is per-module
	// instance; a fresh instance models the next dev run).
	m2, _ := New(Config{ID: "mheal", Name: "Heal", SQL: sqlFS})
	if err := m2.ensureDevAppSchema(ctx, schema); err != nil {
		t.Fatalf("re-provision: %v", err)
	}

	conn, err = pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()
	if !qualifiedTableExists(t, conn.Conn(), schema, "mheal_items") {
		t.Errorf("after self-heal, mheal_items is missing from %s", schema)
	}
}

// TestDevAppSchema_ChecksumDrift_Integration is the regression guard for
// issue #1605: editing an already-applied migration file must be detectable,
// not silently invisible. It proves the checksum recorded at apply time
// changes when the file's bytes change on disk, which is the signal the
// checksum-drift warning in applyDevScope keys off.
func TestDevAppSchema_ChecksumDrift_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	resetDefault(t)
	t.Setenv(devMigrateEnvVar, driftTestDBURL())

	sqlFS := fstest.MapFS{
		"sql/app/0001_init.up.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE IF NOT EXISTS mcksum_items (id text PRIMARY KEY)`),
		},
	}
	m, err := New(Config{ID: "mcksum", Name: "Cksum", SQL: sqlFS})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	pool, release, err := m.resolvePool(ctx)
	if err != nil {
		t.Skipf("skipping (no dev postgres): %v", err)
	}
	defer release()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("skipping (no dev postgres): %v", err)
	}

	schema, _ := devAppSchemaName("cccccccc-0000-0000-0000-000000000003")
	ident := func(s string) string { return pgx.Identifier{s}.Sanitize() }
	cleanup := func() { pool.Exec(ctx, `DROP SCHEMA IF EXISTS `+ident(schema)+` CASCADE`) }
	cleanup()
	t.Cleanup(cleanup)

	if err := m.ensureDevAppSchema(ctx, schema); err != nil {
		t.Fatalf("provision: %v", err)
	}

	var recorded string
	if err := pool.QueryRow(ctx, fmt.Sprintf(
		`SELECT checksum FROM %s.schema_migrations WHERE module_id='mcksum' AND scope='app' AND version='0001'`,
		ident(schema))).Scan(&recorded); err != nil {
		t.Fatalf("read recorded checksum: %v", err)
	}
	if recorded == "" {
		t.Fatal("checksum was not recorded at apply time")
	}

	editedFS := fstest.MapFS{
		"sql/app/0001_init.up.sql": &fstest.MapFile{
			// Same version, different bytes — models editing a released
			// migration in place instead of adding 0002.
			Data: []byte(`CREATE TABLE IF NOT EXISTS mcksum_items (id text PRIMARY KEY, extra text)`),
		},
	}
	editedSum, err := checksumMigration(editedFS, "sql/app/0001_init.up.sql")
	if err != nil {
		t.Fatalf("checksum edited file: %v", err)
	}
	if editedSum == recorded {
		t.Fatal("test fixture is broken: edited file hashes the same as the original")
	}
}
