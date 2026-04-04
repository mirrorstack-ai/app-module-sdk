//go:build integration

package db

import (
	"context"
	"testing"
)

func testDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background())
	if err != nil {
		t.Skipf("skipping: cannot connect to postgres: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

func mustExec(t *testing.T, db *DB, ctx context.Context, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("mustExec %q: %v", sql, err)
	}
}

func TestIntegration_ConnectAndPing(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	conn, release, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer release()

	var result int
	if err := conn.QueryRow(ctx, "SELECT 1").Scan(&result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != 1 {
		t.Errorf("expected 1, got %d", result)
	}
}

func TestIntegration_SchemaIsolation(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	schemas := []struct {
		name string
		data string
	}{
		{"test_app_one", "hello"},
		{"test_app_two", "world"},
	}

	for _, s := range schemas {
		mustExec(t, db, ctx, "DROP SCHEMA IF EXISTS "+s.name+" CASCADE")
		mustExec(t, db, ctx, "CREATE SCHEMA "+s.name)

		schemaCtx := WithSchema(ctx, s.name)
		mustExec(t, db, schemaCtx, "CREATE TABLE items (value TEXT)")
		mustExec(t, db, schemaCtx, "INSERT INTO items (value) VALUES ($1)", s.data)
	}

	t.Cleanup(func() {
		for _, s := range schemas {
			db.Exec(ctx, "DROP SCHEMA IF EXISTS "+s.name+" CASCADE")
		}
	})

	for _, s := range schemas {
		schemaCtx := WithSchema(ctx, s.name)

		conn, release, err := db.Conn(schemaCtx)
		if err != nil {
			t.Fatalf("schema %s: unexpected error: %v", s.name, err)
		}

		var value string
		err = conn.QueryRow(schemaCtx, "SELECT value FROM items LIMIT 1").Scan(&value)
		release()

		if err != nil {
			t.Fatalf("schema %s: unexpected error: %v", s.name, err)
		}
		if value != s.data {
			t.Errorf("schema %s: expected %q, got %q", s.name, s.data, value)
		}
	}
}

func TestIntegration_SchemaResetOnRelease(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	mustExec(t, db, ctx, "DROP SCHEMA IF EXISTS test_leak CASCADE")
	mustExec(t, db, ctx, "CREATE SCHEMA test_leak")
	t.Cleanup(func() {
		db.Exec(ctx, "DROP SCHEMA IF EXISTS test_leak CASCADE")
	})

	schemaCtx := WithSchema(ctx, "test_leak")
	mustExec(t, db, schemaCtx, "CREATE TABLE secret (val TEXT)")
	mustExec(t, db, schemaCtx, "INSERT INTO secret (val) VALUES ('leaked')")

	// Acquire with schema, then release
	conn, release, err := db.Conn(schemaCtx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var val string
	conn.QueryRow(schemaCtx, "SELECT val FROM secret").Scan(&val)
	release() // should reset search_path

	// Acquire WITHOUT schema — should NOT see test_leak tables
	conn2, release2, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer release2()

	err = conn2.QueryRow(ctx, "SELECT val FROM secret").Scan(&val)
	if err == nil {
		t.Error("expected error — schema should not leak to next borrower, but 'secret' table was visible")
	}
}

func TestIntegration_ExecWithSchema(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	mustExec(t, db, ctx, "DROP SCHEMA IF EXISTS test_exec CASCADE")
	mustExec(t, db, ctx, "CREATE SCHEMA test_exec")
	t.Cleanup(func() {
		db.Exec(ctx, "DROP SCHEMA IF EXISTS test_exec CASCADE")
	})

	schemaCtx := WithSchema(ctx, "test_exec")
	mustExec(t, db, schemaCtx, "CREATE TABLE counters (n INT)")
	mustExec(t, db, schemaCtx, "INSERT INTO counters (n) VALUES (42)")

	conn, release, err := db.Conn(schemaCtx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer release()

	var n int
	if err := conn.QueryRow(schemaCtx, "SELECT n FROM counters").Scan(&n); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 42 {
		t.Errorf("expected 42, got %d", n)
	}
}

func TestIntegration_RoleIsolation(t *testing.T) {
	admin := testDB(t)
	ctx := context.Background()

	mustExec(t, admin, ctx, "DROP SCHEMA IF EXISTS test_role_app CASCADE")
	mustExec(t, admin, ctx, "CREATE SCHEMA test_role_app")
	mustExec(t, admin, ctx, "DROP ROLE IF EXISTS mod_media_test")
	mustExec(t, admin, ctx, "DROP ROLE IF EXISTS mod_oauth_test")

	t.Cleanup(func() {
		admin.Exec(ctx, "DROP SCHEMA IF EXISTS test_role_app CASCADE")
		admin.Exec(ctx, "DROP ROLE IF EXISTS mod_media_test")
		admin.Exec(ctx, "DROP ROLE IF EXISTS mod_oauth_test")
	})

	schemaCtx := WithSchema(ctx, "test_role_app")
	mustExec(t, admin, schemaCtx, "CREATE TABLE media_items (id SERIAL, title TEXT)")
	mustExec(t, admin, schemaCtx, "CREATE TABLE oauth_tokens (id SERIAL, token TEXT)")
	mustExec(t, admin, schemaCtx, "INSERT INTO media_items (title) VALUES ('photo.jpg')")
	mustExec(t, admin, schemaCtx, "INSERT INTO oauth_tokens (token) VALUES ('secret123')")

	mustExec(t, admin, ctx, "CREATE ROLE mod_media_test LOGIN PASSWORD 'media'")
	mustExec(t, admin, ctx, "GRANT USAGE ON SCHEMA test_role_app TO mod_media_test")
	mustExec(t, admin, ctx, "GRANT SELECT ON ALL TABLES IN SCHEMA test_role_app TO mod_media_test")
	mustExec(t, admin, ctx, "GRANT ALL ON test_role_app.media_items TO mod_media_test")
	mustExec(t, admin, ctx, "GRANT USAGE ON ALL SEQUENCES IN SCHEMA test_role_app TO mod_media_test")

	mustExec(t, admin, ctx, "CREATE ROLE mod_oauth_test LOGIN PASSWORD 'oauth'")
	mustExec(t, admin, ctx, "GRANT USAGE ON SCHEMA test_role_app TO mod_oauth_test")
	mustExec(t, admin, ctx, "GRANT SELECT ON ALL TABLES IN SCHEMA test_role_app TO mod_oauth_test")
	mustExec(t, admin, ctx, "GRANT ALL ON test_role_app.oauth_tokens TO mod_oauth_test")
	mustExec(t, admin, ctx, "GRANT USAGE ON ALL SEQUENCES IN SCHEMA test_role_app TO mod_oauth_test")

	// Connect as media module
	mediaDB, err := New(ctx, "postgres://mod_media_test:media@localhost:5433/module?sslmode=disable")
	if err != nil {
		t.Fatalf("media connect: %v", err)
	}
	t.Cleanup(mediaDB.Close)

	// Media can read its own table
	conn, release, err := mediaDB.Conn(schemaCtx)
	if err != nil {
		t.Fatalf("media conn: %v", err)
	}
	var title string
	err = conn.QueryRow(schemaCtx, "SELECT title FROM media_items LIMIT 1").Scan(&title)
	release()
	if err != nil {
		t.Fatalf("media read own: %v", err)
	}
	if title != "photo.jpg" {
		t.Errorf("expected 'photo.jpg', got %q", title)
	}

	// Media can READ oauth's table (cross-module join allowed)
	conn, release, err = mediaDB.Conn(schemaCtx)
	if err != nil {
		t.Fatalf("media conn: %v", err)
	}
	var token string
	err = conn.QueryRow(schemaCtx, "SELECT token FROM oauth_tokens LIMIT 1").Scan(&token)
	release()
	if err != nil {
		t.Fatalf("media read oauth: %v", err)
	}
	if token != "secret123" {
		t.Errorf("expected 'secret123', got %q", token)
	}

	// Media can WRITE to its own table
	_, err = mediaDB.Exec(schemaCtx, "INSERT INTO media_items (title) VALUES ('video.mp4')")
	if err != nil {
		t.Fatalf("media write own: %v", err)
	}

	// Media CANNOT write to oauth's table
	_, err = mediaDB.Exec(schemaCtx, "INSERT INTO oauth_tokens (token) VALUES ('hacked')")
	if err == nil {
		t.Error("expected permission denied when media writes to oauth_tokens")
	}
}

func TestIntegration_AppIsolation_CRUD(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	apps := []string{"test_crud_app_a", "test_crud_app_b"}
	for _, schema := range apps {
		mustExec(t, db, ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		mustExec(t, db, ctx, "CREATE SCHEMA "+schema)
		schemaCtx := WithSchema(ctx, schema)
		mustExec(t, db, schemaCtx, "CREATE TABLE items (id SERIAL, title TEXT)")
	}
	t.Cleanup(func() {
		for _, schema := range apps {
			db.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		}
	})

	ctxA := WithSchema(ctx, "test_crud_app_a")
	ctxB := WithSchema(ctx, "test_crud_app_b")

	mustExec(t, db, ctxA, "INSERT INTO items (title) VALUES ('app-a-item')")
	mustExec(t, db, ctxB, "INSERT INTO items (title) VALUES ('app-b-item')")

	// App A sees only its own data
	conn, release, err := db.Conn(ctxA)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	var count int
	err = conn.QueryRow(ctxA, "SELECT COUNT(*) FROM items").Scan(&count)
	release()
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("app A: expected 1 item, got %d", count)
	}

	// App A update doesn't affect App B
	mustExec(t, db, ctxA, "UPDATE items SET title = 'updated'")

	conn, release, err = db.Conn(ctxB)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	var title string
	err = conn.QueryRow(ctxB, "SELECT title FROM items LIMIT 1").Scan(&title)
	release()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if title != "app-b-item" {
		t.Errorf("app B: expected 'app-b-item', got %q", title)
	}

	// App A delete doesn't affect App B
	mustExec(t, db, ctxA, "DELETE FROM items")

	conn, release, err = db.Conn(ctxB)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	err = conn.QueryRow(ctxB, "SELECT COUNT(*) FROM items").Scan(&count)
	release()
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("app B: expected 1 item after app A delete, got %d", count)
	}
}

func TestIntegration_ConnAutoRelease(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		conn, release, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("iteration %d: unexpected error: %v", i, err)
		}
		var result int
		err = conn.QueryRow(ctx, "SELECT 1").Scan(&result)
		release()
		if err != nil {
			t.Fatalf("iteration %d: query error: %v", i, err)
		}
	}
}
