//go:build integration

package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
)

// localPlaneEnv turns ON the co-located-dev plane gate for a test.
//
// MS_LOCAL_DB_URL is doing DOUBLE DUTY here and the trap is easy to miss:
// resultLocal reads it as the "co-located session" gate, and db.devEnvURL reads
// it as the FIRST entry in the pool-URL ladder. Setting it to a dummy value
// would gate the plane ON and the pool OFF, so every local read would fail on
// connect rather than exercising the branch. It therefore has to be a URL that
// actually connects — the same one db.Open just resolved.
func localPlaneEnv(t *testing.T) {
	t.Helper()
	if os.Getenv(devMigrateEnvVar) != "" {
		return // already a co-located session; db.Open resolved this same URL
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skipf("skipping: set %s or DATABASE_URL so the local plane gate and the SDK pool resolve to the same database", devMigrateEnvVar)
	}
	t.Setenv(devMigrateEnvVar, url)
}

// TestDependencyDB_LocalRead_Integration exercises the co-located dev branch end
// to end against a real pool: directory hit → Go-side authorization → derived
// physical name → dynamic SELECT in a READ ONLY tx → normalized rows.
//
// The relation is deliberately declared with `id uuid` and
// `created_at timestamptz`, UNLIKE the deployed test's `id bigint`. That choice
// is the whole point: bigint is the one column type whose native pgx value
// already looks right, so a deployed-shaped fixture would pass even with row
// normalization entirely removed. uuid is the type oauth-core's users.id
// actually is, and the type whose raw [16]byte form silently corrupts the join
// key this surface exists to carry.
func TestDependencyDB_LocalRead_Integration(t *testing.T) {
	setup, err := db.Open(context.Background())
	if err != nil {
		t.Skipf("skipping: cannot connect to postgres: %v", err)
	}
	t.Cleanup(setup.Close)

	localPlaneEnv(t)
	m, err := New(Config{ID: "m1234abcd", Slug: "users-profile"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(m.Close)

	ctx := context.Background()
	// appID and schema are ONE app: resultLocal now cross-checks the trusted
	// auth identity against the schema binding, and both derive through
	// runtime.AppSchemaName. Hard-coding two unrelated literals (as this test
	// originally did) is precisely the divergence the pin refuses.
	const appID = "dep-local-test"
	const schema = "app_dep_local_test"
	const producerID = "m81b3ac7081c1409495700c761e23b59e"
	const physical = producerID + "_users"

	mustExecRaw(t, setup, ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	mustExecRaw(t, setup, ctx, `CREATE SCHEMA `+schema)
	t.Cleanup(func() { _, _ = setup.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })
	mustExecRaw(t, setup, ctx, `CREATE TABLE `+schema+`."`+physical+`" (id uuid, email text, created_at timestamptz)`)
	mustExecRaw(t, setup, ctx, `INSERT INTO `+schema+`."`+physical+`" (id, email, created_at) VALUES
		('1233b3f5-3152-49c3-b3bf-6cd65d870a47', 'a@b.c', now()),
		('a722a8a8-d413-435b-b21b-f4cbacb5ef73', 'c@d.e', now())`)

	// The producer is co-located and self-published "users" as exposed.
	m.devDir.lookup = func(context.Context, string) (devModuleEntry, bool, error) {
		return devModuleEntry{ModuleID: producerID, Slug: "oauth-core", Exposes: []string{"users"}}, true, nil
	}
	// This CONSUMER's own directory row is not what is under test; mark it
	// published so the read path's one-shot self-heal does not write a row into
	// the session-global directory as a side effect.
	m.devDir.published.Store(true)
	// The consumer declared it — both halves of the authorization pair.
	m.registry.AddDependency(registry.Dependency{
		ID: "@mirrorstack/oauth-core", Version: "^0.1", Tables: []string{"users"},
	})

	readCtx := auth.Set(ctx, auth.Identity{AppID: appID})
	readCtx = db.WithSchema(readCtx, schema)

	// Limit(1) over 2 rows → truncated. deployed=false takes the dev plane, and
	// the directory hit takes the local branch inside it.
	res, err := m.DependencyDB(readCtx, "@mirrorstack/oauth-core").
		Select("users").
		Columns("id", "email", "created_at").
		Limit(1).
		result(readCtx, false /* dev plane */)
	if err != nil {
		t.Fatalf("local read: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows = %d, want 1 (cut at limit)", len(res.Rows))
	}
	if !res.Truncated {
		t.Errorf("truncated = false, want true (2 rows, limit 1) — the limit+1 trick must survive the extraction")
	}

	// THE assertion that would have caught the join-key corruption: a uuid
	// column must arrive as a canonical uuid STRING, not [16]uint8.
	id, ok := res.Rows[0]["id"].(string)
	if !ok {
		t.Fatalf("id = %T (%#v), want a Go string — a raw [16]uint8 renders as \"[18 51 179 ...]\" and silently corrupts the join key", res.Rows[0]["id"], res.Rows[0]["id"])
	}
	if len(id) != 36 || strings.Count(id, "-") != 4 {
		t.Errorf("id = %q, want a canonical 8-4-4-4-12 uuid", id)
	}
	if _, ok := res.Rows[0]["created_at"].(string); !ok {
		t.Errorf("created_at = %T, want string (the proxy's RFC3339 form)", res.Rows[0]["created_at"])
	}

	// Fail-closed at read time: dev app schemas are provisioned lazily and
	// INDEPENDENTLY per module, so a missing relation is the failure a developer
	// will actually hit. It must map to ErrDependencyUnavailable and must NAME
	// the schema — without that, "producer relation is not present" is
	// undebuggable when the producer is plainly running.
	mustExecRaw(t, setup, ctx, `DROP TABLE `+schema+`."`+physical+`"`)
	rows, err := m.DependencyDB(readCtx, "@mirrorstack/oauth-core").
		Select("users").Columns("id").Rows(readCtx)
	if rows != nil {
		t.Errorf("rows = %v, want nil after DROP TABLE (never silently empty)", rows)
	}
	if !errors.Is(err, ErrDependencyUnavailable) {
		t.Fatalf("after DROP TABLE: err = %v, want ErrDependencyUnavailable (42P01)", err)
	}
	if !strings.Contains(err.Error(), schema) {
		t.Errorf("err = %v, want the message to name schema %q", err, schema)
	}
}

// TestDependencyDB_LocalNotDeclared_Integration proves authorization precedes
// SQL: the consumer declares nothing, so the read must fail closed BEFORE a
// relation name is ever composed. The table is dropped first, so a leaked read
// would fail with a different error (42P01) rather than passing quietly.
func TestDependencyDB_LocalNotDeclared_Integration(t *testing.T) {
	setup, err := db.Open(context.Background())
	if err != nil {
		t.Skipf("skipping: cannot connect to postgres: %v", err)
	}
	t.Cleanup(setup.Close)

	localPlaneEnv(t)
	m, err := New(Config{ID: "m1234abcd", Slug: "users-profile"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(m.Close)

	ctx := context.Background()
	const appID = "dep-local-undeclared-test"
	const schema = "app_dep_local_undeclared_test"
	const producerID = "m81b3ac7081c1409495700c761e23b59e"

	mustExecRaw(t, setup, ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	mustExecRaw(t, setup, ctx, `CREATE SCHEMA `+schema)
	t.Cleanup(func() { _, _ = setup.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })

	m.devDir.lookup = func(context.Context, string) (devModuleEntry, bool, error) {
		return devModuleEntry{ModuleID: producerID, Slug: "oauth-core", Exposes: []string{"users"}}, true, nil
	}
	m.devDir.published.Store(true)
	// No AddDependency: the producer exposes the table, the consumer never asked.

	readCtx := auth.Set(ctx, auth.Identity{AppID: appID})
	readCtx = db.WithSchema(readCtx, schema)

	rows, err := m.DependencyDB(readCtx, "@mirrorstack/oauth-core").
		Select("users").Columns("id").Rows(readCtx)
	if rows != nil {
		t.Errorf("rows = %v, want nil", rows)
	}
	if !errors.Is(err, ErrNotExposed) {
		t.Fatalf("err = %v, want ErrNotExposed (consumer declared nothing)", err)
	}
	if errors.Is(err, ErrDependencyUnavailable) {
		t.Errorf("err = %v, want the authorization verdict — a 42P01 here means the read ran before authorizing", err)
	}
}

// TestDevDirectory_RoundTrip_Integration exercises for real the one path every
// unit test fakes. Without it the directory's SQL — the DDL, the upsert's
// ON CONFLICT, the jsonb decode, and the `= ANY($1::text[])` lookup across both
// identity columns — would have zero coverage anywhere.
func TestDevDirectory_RoundTrip_Integration(t *testing.T) {
	setup, err := db.Open(context.Background())
	if err != nil {
		t.Skipf("skipping: cannot connect to postgres: %v", err)
	}
	t.Cleanup(setup.Close)

	const producerID = "m81b3ac7081c1409495700c761e23b59e"
	const producerUUID = "81b3ac70-81c1-4094-9570-0c761e23b59e"

	// No plane gate needed: this test drives the directory helpers directly
	// rather than going through resultLocal, and the pool resolves through
	// db.Open's own ladder.
	m, err := New(Config{ID: producerID, Slug: "oauth-core"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(m.Close)
	m.ExposeTable("users")

	ctx := context.Background()
	// Row-scoped cleanup, not table-scoped: the directory is session-global and
	// a live `mirrorstack dev` session may legitimately own other rows in it.
	t.Cleanup(func() {
		_, _ = setup.Exec(context.Background(),
			`DELETE FROM `+devDirectoryTable+` WHERE module_id = $1`, producerID)
	})

	// Idempotence: every co-located module runs this at boot; the first wins.
	if err := m.ensureDevDirectory(ctx); err != nil {
		t.Fatalf("ensureDevDirectory: %v", err)
	}
	if err := m.ensureDevDirectory(ctx); err != nil {
		t.Fatalf("ensureDevDirectory (second call must be a no-op): %v", err)
	}
	if err := m.registerInDevDirectory(ctx); err != nil {
		t.Fatalf("registerInDevDirectory: %v", err)
	}
	// Upsert, not insert: a module reboots on every code change in dev.
	if err := m.registerInDevDirectory(ctx); err != nil {
		t.Fatalf("registerInDevDirectory (re-register must upsert): %v", err)
	}

	// All three ref forms DependencyDB documents must resolve to the same entry.
	for _, ref := range []string{"oauth-core", producerID, producerUUID} {
		t.Run("lookup by "+ref, func(t *testing.T) {
			entry, ok, err := m.readDevDirectory(ctx, ref)
			if err != nil {
				t.Fatalf("readDevDirectory(%q): %v", ref, err)
			}
			if !ok {
				t.Fatalf("readDevDirectory(%q): ok = false, want a hit", ref)
			}
			if entry.ModuleID != producerID {
				t.Errorf("ModuleID = %q, want %q", entry.ModuleID, producerID)
			}
			if entry.Slug != "oauth-core" {
				t.Errorf("Slug = %q, want oauth-core", entry.Slug)
			}
			if len(entry.Exposes) != 1 || entry.Exposes[0] != "users" {
				t.Errorf("Exposes = %v, want [users]", entry.Exposes)
			}
		})
	}

	// A miss is (_, false, nil) — the fallthrough signal, never an error.
	entry, ok, err := m.readDevDirectory(ctx, "definitely-not-running")
	if err != nil {
		t.Errorf("readDevDirectory(miss): err = %v, want nil (a miss is not an error)", err)
	}
	if ok {
		t.Errorf("readDevDirectory(miss): ok = true, want false (got %+v)", entry)
	}
}

// TestDevDirectory_StaleRowExpires_Integration is the FAIL-CLOSED lock for
// DEFECT 3, and it needs a real database because the freshness bound is
// expressed in SQL against the row's own now()-written updated_at.
//
// The invariant: a directory hit means "co-located RIGHT NOW". Before the lease
// existed a row was written at boot and never invalidated, so a producer that
// had STOPPED still won the lookup — and because its relation is still present
// in the app schema (stopping a module does not drop its tables), the local read
// then SUCCEEDED and returned whatever the dead producer last wrote, usually
// zero rows with err == nil. A silent empty, which this SDK forbids, reached by
// treating a remote producer as co-located. Expiry must degrade to a MISS, which
// is the additive proxy fallthrough.
func TestDevDirectory_StaleRowExpires_Integration(t *testing.T) {
	setup, err := db.Open(context.Background())
	if err != nil {
		t.Skipf("skipping: cannot connect to postgres: %v", err)
	}
	t.Cleanup(setup.Close)

	const producerID = "m81b3ac7081c1409495700c761e23b59e"
	m, err := New(Config{ID: producerID, Slug: "oauth-core"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(m.Close)
	m.ExposeTable("users")

	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = setup.Exec(context.Background(),
			`DELETE FROM `+devDirectoryTable+` WHERE module_id = $1`, producerID)
	})
	if err := m.publishDevDirectory(ctx); err != nil {
		t.Fatalf("publishDevDirectory: %v", err)
	}

	// Fresh lease: a hit.
	if _, ok, err := m.readDevDirectory(ctx, "oauth-core"); err != nil || !ok {
		t.Fatalf("fresh lease: (ok=%v, err=%v), want a hit", ok, err)
	}

	// Age the row past the TTL, in DB time — exactly what a producer that
	// stopped running looks like once its heartbeats stop.
	if _, err := setup.Exec(ctx,
		`UPDATE `+devDirectoryTable+` SET updated_at = now() - make_interval(secs => $1) WHERE module_id = $2`,
		devDirectoryTTL.Seconds()+60, producerID); err != nil {
		t.Fatalf("age the row: %v", err)
	}

	entry, ok, err := m.readDevDirectory(ctx, "oauth-core")
	if err != nil {
		t.Fatalf("expired lease: err = %v, want nil (expiry is a miss, not an error)", err)
	}
	if ok {
		t.Fatalf("expired lease: ok = true (%+v) — a producer that stopped running must NOT resolve as co-located; the local read would succeed against its abandoned tables and hand the consumer a silent empty", entry)
	}

	// And a heartbeat brings it straight back: expiry is a liveness bound, not
	// a one-way tombstone.
	if err := m.publishDevDirectory(ctx); err != nil {
		t.Fatalf("heartbeat re-publish: %v", err)
	}
	if _, ok, err := m.readDevDirectory(ctx, "oauth-core"); err != nil || !ok {
		t.Fatalf("after heartbeat: (ok=%v, err=%v), want a hit again", ok, err)
	}
}

// TestDevDirectory_SlugReclamation_Integration covers the other half of
// DEFECT 3: without it, a module whose Config.ID changes across dev sessions
// leaves its old id answering to the slug forever. readDevDirectory then sees
// two rows for one ref and refuses the lookup as ambiguous, silently demoting a
// working session to the proxy — and the table grows a row per session, forever.
func TestDevDirectory_SlugReclamation_Integration(t *testing.T) {
	setup, err := db.Open(context.Background())
	if err != nil {
		t.Skipf("skipping: cannot connect to postgres: %v", err)
	}
	t.Cleanup(setup.Close)

	const oldID = "m00000000000000000000000000000001"
	const newID = "m00000000000000000000000000000002"
	const slug = "reclaim-test"

	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = setup.Exec(context.Background(),
			`DELETE FROM `+devDirectoryTable+` WHERE slug = $1`, slug)
	})

	oldM, err := New(Config{ID: oldID, Slug: slug})
	if err != nil {
		t.Fatalf("New(old): %v", err)
	}
	t.Cleanup(oldM.Close)
	if err := oldM.publishDevDirectory(ctx); err != nil {
		t.Fatalf("publish(old): %v", err)
	}

	// The same module, re-registered with a new platform-minted id.
	newM, err := New(Config{ID: newID, Slug: slug})
	if err != nil {
		t.Fatalf("New(new): %v", err)
	}
	t.Cleanup(newM.Close)
	if err := newM.publishDevDirectory(ctx); err != nil {
		t.Fatalf("publish(new): %v", err)
	}

	var rows int
	if err := setup.Pool().QueryRow(ctx,
		`SELECT count(*) FROM `+devDirectoryTable+` WHERE slug = $1`, slug).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Errorf("slug %q has %d rows, want 1 — the superseded module_id was not reclaimed", slug, rows)
	}

	entry, ok, err := newM.readDevDirectory(ctx, slug)
	if err != nil {
		t.Fatalf("lookup by slug: %v (two claimants would make this ambiguous)", err)
	}
	if !ok || entry.ModuleID != newID {
		t.Errorf("lookup resolved to %+v (ok=%v), want the current id %q", entry, ok, newID)
	}
}

// TestEnsureDevDirectory_Concurrent_Integration is the DEFECT 2 race, run for
// real. `mirrorstack dev --all` boots every module at the same instant, and
// CREATE TABLE IF NOT EXISTS is idempotent but NOT concurrency-safe — its
// existence check and catalog insert are not atomic, so interleaved backends
// raise 23505 or 42P07. Every one of these calls must report success, because
// the postcondition (the relation exists) holds in every case.
func TestEnsureDevDirectory_Concurrent_Integration(t *testing.T) {
	setup, err := db.Open(context.Background())
	if err != nil {
		t.Skipf("skipping: cannot connect to postgres: %v", err)
	}
	t.Cleanup(setup.Close)

	const n = 12
	mods := make([]*Module, n)
	for i := range mods {
		m, err := New(Config{ID: fmt.Sprintf("mconcurrent%020d", i), Slug: "conc-test"})
		if err != nil {
			t.Fatalf("New(%d): %v", i, err)
		}
		t.Cleanup(m.Close)
		mods[i] = m
	}

	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, m := range mods {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release them all at the same instant
			errs[i] = m.ensureDevDirectory(context.Background())
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("ensureDevDirectory[%d] = %v; a concurrent CREATE must report success — the relation exists either way", i, err)
		}
	}

	var exists bool
	if err := setup.Pool().QueryRow(context.Background(),
		`SELECT to_regclass($1) IS NOT NULL`, devDirectoryTable).Scan(&exists); err != nil {
		t.Fatalf("to_regclass: %v", err)
	}
	if !exists {
		t.Fatal("the directory table does not exist after 12 concurrent ensures")
	}
}
