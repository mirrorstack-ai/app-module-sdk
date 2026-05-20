package core

import (
	"context"
	"testing"
	"testing/fstest"
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

// TestApplyDevMigrations_Integration verifies the full dev-migrate path
// against a live Postgres. Skipped when no Postgres is reachable — matches
// the integration-test convention in db/db_integration_test.go.
//
// The migration here uses the same `CREATE TABLE IF NOT EXISTS` shape the
// CLI scaffold ships, so a re-run after a successful first run is a true
// idempotency check (tracking table prevents Apply from running them again).
func TestApplyDevMigrations_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	resetDefault(t)
	t.Setenv(devMigrateEnvVar, "postgres://mirrorstack:mirrorstack@localhost:5433/mirrorstack?sslmode=disable")

	sqlFS := fstest.MapFS{
		"sql/app/0001_init.up.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE IF NOT EXISTS dev_migrate_test_items (id text PRIMARY KEY)`),
		},
	}

	m, err := New(Config{ID: "devmigtest", Name: "Dev Migrate Test", SQL: sqlFS})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	if err := m.applyDevMigrations(ctx); err != nil {
		t.Skipf("skipping (no postgres on :5433): %v", err)
	}

	// Second call should be a no-op (tracking table records the prior run).
	// If Apply runs again on an idempotent migration the test still passes;
	// if it ran on a non-idempotent migration it would fail — the point
	// is to assert the no-op path is taken, which we observe by reading
	// the tracking table.
	if err := m.applyDevMigrations(ctx); err != nil {
		t.Fatalf("second applyDevMigrations: %v", err)
	}

	pool, release, err := m.resolvePool(ctx)
	if err != nil {
		t.Fatalf("resolvePool: %v", err)
	}
	defer release()

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM __ms_local.schema_migrations WHERE scope = 'app' AND version = '0001'`,
	).Scan(&count); err != nil {
		t.Fatalf("read tracking row: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 tracking row for app/0001, got %d", count)
	}

	// Cleanup so reruns are deterministic.
	if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS dev_migrate_test_items`); err != nil {
		t.Logf("cleanup table: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM __ms_local.schema_migrations WHERE scope = 'app' AND version = '0001'`,
	); err != nil {
		t.Logf("cleanup tracking: %v", err)
	}
}
