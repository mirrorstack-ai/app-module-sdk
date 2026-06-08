package core

import (
	"context"
	"fmt"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
)

func TestDevMigrateEnabled(t *testing.T) {
	cases := []struct {
		name       string
		localDBURL string
		want       bool
	}{
		{"unset → off", "", false},
		{"set → on", "postgres://x@y:1/z", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetDefault(t)
			t.Setenv(devMigrateEnvVar, tc.localDBURL)
			m := newTestModuleWithSecret(t, "media")
			if got := m.devMigrateEnabled(); got != tc.want {
				t.Errorf("devMigrateEnabled() with MS_LOCAL_DB_URL=%q: got %v, want %v", tc.localDBURL, got, tc.want)
			}
		})
	}
}

func TestDevAppSchemaName(t *testing.T) {
	cases := []struct {
		appID string
		want  string
		ok    bool
	}{
		{"f64152df-77ff-4552-b31a-78026ea49cb7", "app_f64152df_77ff_4552_b31a_78026ea49cb7", true},
		{"63B23941-245C-43FA-A68E-979E4997FBFE", "app_63b23941_245c_43fa_a68e_979e4997fbfe", true},
		{"local-dev-app", "app_local_dev_app", true},
		{"bad space", "", false},
		{"semi;colon", "", false},
		{"", "app_", false}, // "app_" has no trailing chars → fails the +-quantifier pattern
	}
	for _, tc := range cases {
		got, ok := devAppSchemaName(tc.appID)
		if ok != tc.ok {
			t.Errorf("devAppSchemaName(%q) ok = %v, want %v", tc.appID, ok, tc.ok)
		}
		if ok && got != tc.want {
			t.Errorf("devAppSchemaName(%q) = %q, want %q", tc.appID, got, tc.want)
		}
	}
}

// TestDevAppSchema_PerAppIsolation_Integration is the regression guard for the
// dev per-app isolation bug: every app must get its OWN app_<id> schema, so the
// same module installed on two apps does not share rows. Runs against the live
// dev Postgres on :5433 (matches db/db_integration_test.go conventions);
// skipped in short mode or when Postgres is unreachable.
func TestDevAppSchema_PerAppIsolation_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	resetDefault(t)
	t.Setenv(devMigrateEnvVar, "postgres://mirrorstack:mirrorstack@localhost:5433/mirrorstack?sslmode=disable")

	sqlFS := fstest.MapFS{
		"sql/app/0001_init.up.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE IF NOT EXISTS dev_iso_items (id text PRIMARY KEY)`),
		},
	}

	m, err := New(Config{ID: "devisotest", Name: "Dev Iso Test", SQL: sqlFS})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	pool, release, err := m.resolvePool(ctx)
	if err != nil {
		t.Skipf("skipping (no postgres on :5433): %v", err)
	}
	defer release()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("skipping (no postgres on :5433): %v", err)
	}

	schemaA, _ := devAppSchemaName("11111111-1111-1111-1111-111111111111")
	schemaB, _ := devAppSchemaName("22222222-2222-2222-2222-222222222222")
	ident := func(s string) string { return pgx.Identifier{s}.Sanitize() }

	cleanup := func() {
		for _, s := range []string{schemaA, schemaB} {
			if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS `+ident(s)+` CASCADE`); err != nil {
				t.Logf("cleanup %s: %v", s, err)
			}
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	// Lazily provision both apps, as the dev middleware does on first request.
	if err := m.ensureDevAppSchema(ctx, schemaA); err != nil {
		t.Fatalf("provision %s: %v", schemaA, err)
	}
	if err := m.ensureDevAppSchema(ctx, schemaB); err != nil {
		t.Fatalf("provision %s: %v", schemaB, err)
	}

	// Write a row through app A's schema; app B must NOT see it.
	if _, err := pool.Exec(ctx, fmt.Sprintf(`INSERT INTO %s.dev_iso_items(id) VALUES('a1')`, ident(schemaA))); err != nil {
		t.Fatalf("insert into %s: %v", schemaA, err)
	}
	countIn := func(schema string) int {
		var n int
		if err := pool.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s.dev_iso_items`, ident(schema))).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", schema, err)
		}
		return n
	}
	if got := countIn(schemaA); got != 1 {
		t.Errorf("app A rows = %d, want 1", got)
	}
	if got := countIn(schemaB); got != 0 {
		t.Errorf("app B rows = %d, want 0 — per-app isolation breach", got)
	}

	// Re-provisioning A is a cached no-op and the tracking table holds exactly
	// one row for the applied migration.
	if err := m.ensureDevAppSchema(ctx, schemaA); err != nil {
		t.Fatalf("re-provision %s: %v", schemaA, err)
	}
	var trk int
	if err := pool.QueryRow(ctx,
		fmt.Sprintf(`SELECT count(*) FROM %s.schema_migrations WHERE scope = 'app' AND version = '0001'`, ident(schemaA)),
	).Scan(&trk); err != nil {
		t.Fatalf("read tracking row: %v", err)
	}
	if trk != 1 {
		t.Errorf("expected exactly 1 tracking row for app/0001 in %s, got %d", schemaA, trk)
	}
}
