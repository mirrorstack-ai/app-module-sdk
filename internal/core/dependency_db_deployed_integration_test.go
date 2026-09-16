//go:build integration

package core

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/db"
)

// TestDependencyDB_DeployedRead_Integration exercises the deployed branch
// (decision 18 §3) end to end against a real pool: it composes the dynamic
// SELECT from an injected manifest, runs it in a READ ONLY tx as the pool's
// role, and returns fetch-then-join rows + a Truncated flag. It also proves the
// §5 layer-2 fail-closed: dropping the physical relation surfaces 42P01 as
// ErrDependencyUnavailable, never a silent empty.
//
// Uses the SDK dev pool (resolvePool with no ctx credential) as the consumer
// role — the GRANT ceiling is exercised by the Phase-2 platform E2E
// (decision 18 §7 step 2), not here; this test locks the SDK read mechanics.
func TestDependencyDB_DeployedRead_Integration(t *testing.T) {
	setup, err := db.Open(context.Background())
	if err != nil {
		t.Skipf("skipping: cannot connect to postgres: %v", err)
	}
	t.Cleanup(setup.Close)

	m, err := New(Config{ID: "m1234abcd"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(m.Close)

	ctx := context.Background()
	const schema = "app_dep_deployed_test"
	const physical = "m81b3ac7081c1409495700c761e23b59e_users"

	mustExecRaw(t, setup, ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	mustExecRaw(t, setup, ctx, `CREATE SCHEMA `+schema)
	t.Cleanup(func() { _, _ = setup.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })
	mustExecRaw(t, setup, ctx, `CREATE TABLE `+schema+`."`+physical+`" (id bigint, email text, deleted_at timestamptz)`)
	mustExecRaw(t, setup, ctx, `INSERT INTO `+schema+`."`+physical+`" (id, email) VALUES (9007199254740993, 'a@b.c'), (2, 'c@d.e')`)

	readCtx := auth.Set(ctx, auth.Identity{AppID: "app-uuid-1"})
	readCtx = db.WithSchema(readCtx, schema)
	readCtx = db.WithDependencies(readCtx, []db.DependencyGrant{
		{Ref: "oauth-core", Tables: map[string]string{"users": physical}},
	})

	// Limit(1) over 2 rows → truncated. Force the deployed branch via the seam.
	res, err := m.DependencyDB(readCtx, "@mirrorstack/oauth-core").
		Select("users").
		Columns("id", "email", "deleted_at").
		Limit(1).
		result(readCtx, true /* deployed */)
	if err != nil {
		t.Fatalf("deployed read: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows = %d, want 1 (cut at limit)", len(res.Rows))
	}
	if !res.Truncated {
		t.Errorf("truncated = false, want true (2 rows, limit 1)")
	}
	// pgx.RowToMap decodes bigint natively as int64 — full fidelity for a join
	// key that would round-trip lossily as float64. #31's stringField reads it
	// via its default fmt.Sprint branch.
	switch v := res.Rows[0]["id"].(type) {
	case int64:
		if v != 9007199254740993 {
			t.Errorf("id = %d, want 9007199254740993", v)
		}
	case json.Number:
		if v.String() != "9007199254740993" {
			t.Errorf("id = %s, want 9007199254740993", v)
		}
	default:
		t.Errorf("id decoded as %T, want int64 (native) or json.Number", res.Rows[0]["id"])
	}

	// §5 layer 2: drop the relation → 42P01 → ErrDependencyUnavailable.
	mustExecRaw(t, setup, ctx, `DROP TABLE `+schema+`."`+physical+`"`)
	_, err = m.DependencyDB(readCtx, "@mirrorstack/oauth-core").
		Select("users").Columns("id").result(readCtx, true)
	if !errors.Is(err, ErrDependencyUnavailable) {
		t.Errorf("after DROP TABLE: err = %v, want ErrDependencyUnavailable (42P01)", err)
	}
}

func mustExecRaw(t *testing.T, d *db.DB, ctx context.Context, sql string) {
	t.Helper()
	if _, err := d.Exec(ctx, sql); err != nil {
		t.Fatalf("setup exec %q: %v", sql, err)
	}
}

// TestDependencyDB_DeployedRead_UUIDColumn_Integration is the regression that
// only a real Postgres can carry: a uuid PRIMARY KEY must reach the consumer as
// the canonical text it was inserted as.
//
// Deleting normalizeUUIDValues from queryDynamicSelect makes this test fail
// with [16]uint8, which is exactly what production returned on 2026-09-16:
// video-watched's `row["id"].(string)` yielded "", every measurable video was
// skipped on the empty id, and a completed video reported "no duration
// recorded" because its facts entry was keyed under "". A fixture cannot prove
// this — the dev/proxy plane JSON-encodes a uuid as a string, so the shape under
// test never appears there.
func TestDependencyDB_DeployedRead_UUIDColumn_Integration(t *testing.T) {
	setup, err := db.Open(context.Background())
	if err != nil {
		t.Skipf("skipping: cannot connect to postgres: %v", err)
	}
	t.Cleanup(setup.Close)

	m, err := New(Config{ID: "m1234abcd"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(m.Close)

	ctx := context.Background()
	const schema = "app_dep_uuid_test"
	const physical = "m81b3ac7081c1409495700c761e23b59e_videos"
	const videoID = "3bc6dbe1-c5b8-4d77-9da5-2deb1ccccf4e"

	mustExecRaw(t, setup, ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	mustExecRaw(t, setup, ctx, `CREATE SCHEMA `+schema)
	t.Cleanup(func() { _, _ = setup.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })
	mustExecRaw(t, setup, ctx, `CREATE TABLE `+schema+`."`+physical+`" (id uuid PRIMARY KEY, title text NOT NULL, duration_sec double precision, owner_id uuid)`)
	mustExecRaw(t, setup, ctx, `INSERT INTO `+schema+`."`+physical+`" (id, title, duration_sec, owner_id) VALUES ('`+videoID+`', 'a video', 154.2, NULL)`)

	readCtx := auth.Set(ctx, auth.Identity{AppID: "app-uuid-1"})
	readCtx = db.WithSchema(readCtx, schema)
	readCtx = db.WithDependencies(readCtx, []db.DependencyGrant{
		{Ref: "video-core", Tables: map[string]string{"videos": physical}},
	})

	res, err := m.DependencyDB(readCtx, "@mirrorstack/video-core").
		Select("videos").
		Columns("id", "title", "duration_sec", "owner_id").
		WhereIn("id", videoID).
		result(readCtx, true /* deployed */)
	if err != nil {
		t.Fatalf("deployed read: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(res.Rows))
	}

	// The assertion the consumer actually writes, against a map[string]any.
	id, ok := res.Rows[0]["id"].(string)
	if !ok {
		t.Fatalf(`row["id"].(string) failed: id decoded as %T (%#v) — a consumer reads this as "" and drops the row`, res.Rows[0]["id"], res.Rows[0]["id"])
	}
	if id != videoID {
		t.Errorf("id = %q, want %q", id, videoID)
	}
	if got, ok := res.Rows[0]["duration_sec"].(float64); !ok || got != 154.2 {
		t.Errorf("duration_sec = %#v (%T), want 154.2", res.Rows[0]["duration_sec"], res.Rows[0]["duration_sec"])
	}
	// A NULL uuid stays absent rather than becoming the all-zero uuid, which
	// would read as a real owner.
	if res.Rows[0]["owner_id"] != nil {
		t.Errorf("owner_id = %#v, want nil for a NULL uuid", res.Rows[0]["owner_id"])
	}
}
