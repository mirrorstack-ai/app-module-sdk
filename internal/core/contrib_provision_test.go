package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/migration"
)

// contribTestPayload is the shape a host declares for its slot; the register
// route validates against it with DisallowUnknownFields.
type contribTestPayload struct {
	Label string `json:"label"`
}

// unreachableDSN is a DSN whose connect fails immediately (nothing listens on
// port 1), so "did this code path touch Postgres at all?" becomes a testable
// question without needing a database.
const unreachableDSN = "postgres://u:p@127.0.0.1:1/none?sslmode=disable"

const contributionDefaultDevDSN = "postgres://mirrorstack:mirrorstack@localhost:5433/mirrorstack?sslmode=disable"

// provisionIntegrationDSN uses the same DATABASE_URL contract as the CI
// integration job. Developers without one configured retain the historical
// localhost:5433 fallback.
func provisionIntegrationDSN() (dsn string, required bool) {
	if dsn := strings.TrimSpace(os.Getenv("DATABASE_URL")); dsn != "" {
		return dsn, true
	}
	return contributionDefaultDevDSN, false
}

// requireProvisionPostgres only skips when a developer has not configured a
// database. A supplied DATABASE_URL is an explicit integration-test contract,
// so a connection failure must fail CI instead of silently reducing coverage.
func requireProvisionPostgres(t *testing.T, required bool, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if required {
		t.Fatalf("configured DATABASE_URL is unavailable: %v", err)
	}
	t.Skipf("skipping (no DATABASE_URL and default Postgres is unavailable): %v", err)
}

func TestLifecycleProvisioner_AppScopeOnly(t *testing.T) {
	resetDefault(t)
	m := newTestModuleWithSecret(t, "provscope")

	if p := m.lifecycleProvisioner(migration.ScopeModule); p != nil {
		t.Error("module scope got a provisioner — the contributions store is per-APP, mod_<id> is shared across apps")
	}
	if p := m.lifecycleProvisioner(migration.ScopeApp); p == nil {
		t.Error("app scope got no provisioner")
	}
}

// The app-scope provisioner ALWAYS reaches the database, with or without
// contribution slots.
//
// 🔴 THIS TEST USED TO ASSERT THE OPPOSITE, and the assertion was correct until
// the audit outbox joined the ensure step. Contribution storage is only needed
// by a module that declares slots; the audit outbox is needed by EVERY module,
// because any module may record audit. So "no slots means no database work" is
// no longer true, and pinning it would pin the outbox out of existence for
// exactly the modules that declare no slots.
//
// What the original protected is still protected, in the code rather than here:
// m.contribReg.Len() is read INSIDE the returned closure, never when the route
// is mounted. ms.Provide runs after ms.Init — which is what mounts the
// lifecycle routes — so a mount-time read would see zero slots for every module
// that declares any, and silently provision nothing forever. That ordering is
// no longer observable through this seam, because both branches now touch the
// database.
func TestLifecycleProvisioner_AlwaysEnsuresAudit(t *testing.T) {
	resetDefault(t)
	t.Setenv(devMigrateEnvVar, "")
	t.Setenv("DATABASE_URL", unreachableDSN)
	m := newTestModuleWithSecret(t, "provslots")

	// Captured exactly as mountSystemRoutes captures it: before any Provide.
	provision := m.lifecycleProvisioner(migration.ScopeApp)
	ctx := db.WithSchema(context.Background(), "app_11111111_1111_1111_1111_111111111111")

	if err := provision(ctx); err == nil {
		t.Error("with no slots the hook must still reach the DB for the audit outbox, got nil")
	}

	m.ProvideSlot(NewContributionSlot[contribTestPayload]("auth-provider"))
	if err := provision(ctx); err == nil {
		t.Error("after ms.Provide the hook must reach the DB (unreachable here), got nil")
	}
}

// TestLifecycleProvisioner_SkipsBareTunnelBody covers the dev-tunnel payload
// shape (no schema, no credential): provisioning there would create a PER-APP
// table in the connection's default schema, shared by every app. The dev plane
// creates it per app schema in provisionDevAppSchema instead.
func TestLifecycleProvisioner_SkipsBareTunnelBody(t *testing.T) {
	resetDefault(t)
	t.Setenv(devMigrateEnvVar, "")
	t.Setenv("DATABASE_URL", unreachableDSN)
	m := newTestModuleWithSecret(t, "provbare")
	m.ProvideSlot(NewContributionSlot[contribTestPayload]("auth-provider"))

	if err := m.lifecycleProvisioner(migration.ScopeApp)(context.Background()); err != nil {
		t.Fatalf("schema-less lifecycle body: want a no-op, got %v", err)
	}
}

// TestContributionStore_DeployedPlane_Integration is the end-to-end proof that
// the store exists on the DEPLOYED path — the plane where Start() returns into
// lambda.Start and nothing below it ever runs.
//
// Deployed-plane fidelity: MS_LOCAL_DB_URL is unset (so devMode is false and the
// dev per-app middleware is NOT mounted), the app schema arrives the way the
// platform sends it — in the lifecycle body, and in the request context the way
// the Lambda shim injects it — and every call goes through the real mounted
// routes. The one substitution is the DB credential: the connection is the local
// superuser rather than a minted r_<app>_<mod> role, because only the platform
// can mint those. system's TestInstallHandler_ProvisionRunsInLifecycleContext
// covers the credential half of the same body.
//
// Runs against DATABASE_URL when configured (including CI), otherwise the dev
// Postgres on :5433. The fallback may skip when unavailable; a configured
// database must be reachable.
func TestContributionStore_DeployedPlane_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	resetDefault(t)
	t.Setenv(devMigrateEnvVar, "")
	dsn, databaseRequired := provisionIntegrationDSN()
	t.Setenv("DATABASE_URL", dsn)

	// A platform-minted module id (m + 32 hex) so the physical table name — and
	// the dev cross-module guard's view of it — is the production shape.
	const moduleID = "m9f1c4b2a7d8e4f0ab1c2d3e4f5061728"
	m := newTestModuleWithSecret(t, moduleID)
	if m.devMode {
		t.Fatal("devMode is true — this test must exercise the non-dev plane")
	}
	// Declared AFTER New, exactly as a module's main.go does it.
	m.ProvideSlot(NewContributionSlot[contribTestPayload]("auth-provider"))

	ctx := context.Background()
	pool, release, err := m.resolvePool(ctx)
	requireProvisionPostgres(t, databaseRequired, err)
	defer release()
	requireProvisionPostgres(t, databaseRequired, pool.Ping(ctx))

	appInstalled := "aaaaaaaa-1111-1111-1111-111111111111"  // gets the install hook
	appLegacy := "bbbbbbbb-2222-2222-2222-222222222222"     // installed before the hook existed
	appConcurrent := "cccccccc-3333-3333-3333-333333333333" // two cold starts at once
	schemas := map[string]string{}
	ident := func(s string) string { return pgx.Identifier{s}.Sanitize() }
	for _, id := range []string{appInstalled, appLegacy, appConcurrent} {
		s, ok := runtimeAppSchema(t, id)
		if !ok {
			t.Fatalf("derive schema for %s", id)
		}
		schemas[id] = s
	}
	cleanup := func() {
		for _, s := range schemas {
			if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS `+ident(s)+` CASCADE`); err != nil {
				t.Logf("cleanup %s: %v", s, err)
			}
		}
		if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS public.`+ident(moduleID+"_contributions")); err != nil {
			t.Logf("cleanup public leak check: %v", err)
		}
	}
	cleanup()
	t.Cleanup(cleanup)
	// The platform owns the app schema; it exists before any lifecycle call.
	for _, s := range schemas {
		if _, err := pool.Exec(ctx, `CREATE SCHEMA `+ident(s)); err != nil {
			t.Fatalf("create schema %s: %v", s, err)
		}
	}

	table := moduleID + "_contributions"
	tableIn := func(schema string) bool {
		var exists bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.tables
				WHERE table_schema = $1 AND table_name = $2 AND table_type = 'BASE TABLE'
			)`, schema, table).Scan(&exists); err != nil {
			t.Fatalf("table lookup in %s: %v", schema, err)
		}
		return exists
	}

	// --- Phase 1: the platform's deployed install POST creates the store -----
	if code, body := postInstall(t, m, appInstalled, schemas[appInstalled]); code != http.StatusOK {
		t.Fatalf("install status = %d, body = %s", code, body)
	}
	if !tableIn(schemas[appInstalled]) {
		t.Fatalf("%s.%s missing after install — the deployed plane still has no contribution store", schemas[appInstalled], table)
	}
	if tableIn("public") {
		t.Errorf("public.%s exists — a PER-APP table landed in the default schema", table)
	}
	if tableIn(schemas[appLegacy]) {
		t.Errorf("installing app %s also created the store in %s — the store is not app-scoped", appInstalled, schemas[appLegacy])
	}
	var idxCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_indexes WHERE schemaname = $1 AND indexname = $2`,
		schemas[appInstalled], table+"_slot_registered_idx").Scan(&idxCount); err != nil {
		t.Fatalf("index lookup: %v", err)
	}
	if idxCount != 1 {
		t.Errorf("slot/registered_at index count = %d, want 1 — the statement after the advisory lock did not run", idxCount)
	}

	// Re-running install is idempotent (the platform re-POSTs install on a dev
	// refresh and on a retry after a partial failure).
	if code, body := postInstall(t, m, appInstalled, schemas[appInstalled]); code != http.StatusOK {
		t.Fatalf("second install status = %d, body = %s", code, body)
	}

	// --- Phase 2: the store is usable and app-scoped through the real routes -
	contributor := "dddddddd-4444-4444-a444-444444444444"
	if code, body := postContribution(t, m, schemas[appInstalled], contributor, `{"label":"Google"}`); code != http.StatusOK {
		t.Fatalf("register status = %d, body = %s", code, body)
	}
	if got := listContributions(t, m, schemas[appInstalled]); len(got) != 1 || got[0] != contributor {
		t.Errorf("app %s contributions = %v, want [%s]", appInstalled, got, contributor)
	}

	// --- Phase 3: a read heals an install that predates the hook -------------
	// This app never received an install/upgrade call carrying the hook, which is
	// every app already installed when this ships: the platform sends no
	// lifecycle call at all once the migration watermark is at target.
	stored, err := m.StoredContributions(db.WithSchema(ctx, schemas[appLegacy]), "auth-provider")
	if err != nil {
		t.Fatalf("StoredContributions on an unprovisioned app: %v", err)
	}
	if len(stored) != 0 {
		t.Errorf("stored = %v, want empty for an app nobody contributed to", stored)
	}
	if !tableIn(schemas[appLegacy]) {
		t.Errorf("%s.%s missing — the read did not heal the unprovisioned app", schemas[appLegacy], table)
	}
	// Healed, and still isolated: app A's row is not visible here.
	if got := listContributions(t, m, schemas[appLegacy]); len(got) != 0 {
		t.Errorf("app %s contributions = %v, want none — rows leaked across app schemas", appLegacy, got)
	}

	// --- Phase 4: concurrent provisioning of ONE app does not fight ----------
	// Two cold containers serving the same app race on CREATE TABLE IF NOT
	// EXISTS, which is idempotent but not atomic. All of them must succeed.
	const racers = 8
	var wg sync.WaitGroup
	codes := make([]int, racers)
	bodies := make([]string, racers)
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], bodies[i] = postInstall(t, m, appConcurrent, schemas[appConcurrent])
		}()
	}
	wg.Wait()
	for i, code := range codes {
		if code != http.StatusOK {
			t.Errorf("concurrent install %d status = %d, body = %s", i, code, bodies[i])
		}
	}
	if !tableIn(schemas[appConcurrent]) {
		t.Errorf("%s.%s missing after %d concurrent installs", schemas[appConcurrent], table, racers)
	}
}

// TestContributionStore_DevPlaneUnchanged_Integration is the "do not disturb
// dev" guard for the two dev-facing halves of this change: the per-app store is
// still created by the dev middleware's lazy provisioning, and the dev tunnel's
// bare install body (no schema, no credential — what the platform sends over a
// tunnel with a service token) still creates nothing, rather than dropping a
// per-app table into the shared default schema.
func TestContributionStore_DevPlaneUnchanged_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	resetDefault(t)
	dsn, databaseRequired := provisionIntegrationDSN()
	t.Setenv(devMigrateEnvVar, dsn)

	const moduleID = "m3a2b1c09876543210fedcba987654321"
	m := newTestModuleWithSecret(t, moduleID)
	if !m.devMode {
		t.Fatal("devMode is false — this test must exercise the dev plane")
	}
	m.ProvideSlot(NewContributionSlot[contribTestPayload]("auth-provider"))

	ctx := context.Background()
	pool, release, err := m.resolvePool(ctx)
	requireProvisionPostgres(t, databaseRequired, err)
	defer release()
	requireProvisionPostgres(t, databaseRequired, pool.Ping(ctx))

	schema, _ := devAppSchemaName("eeeeeeee-5555-5555-5555-555555555555")
	table := moduleID + "_contributions"
	ident := func(s string) string { return pgx.Identifier{s}.Sanitize() }
	cleanup := func() {
		if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS `+ident(schema)+` CASCADE`); err != nil {
			t.Logf("cleanup %s: %v", schema, err)
		}
		if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS public.`+ident(table)); err != nil {
			t.Logf("cleanup public leak check: %v", err)
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	if err := m.ensureDevAppSchema(ctx, schema); err != nil {
		t.Fatalf("dev provision %s: %v", schema, err)
	}
	inSchema := func(s string) bool {
		var exists bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.tables
				WHERE table_schema = $1 AND table_name = $2
			)`, s, table).Scan(&exists); err != nil {
			t.Fatalf("table lookup in %s: %v", s, err)
		}
		return exists
	}
	if !inSchema(schema) {
		t.Errorf("%s.%s missing — dev lazy provisioning regressed", schema, table)
	}

	// The tunnel install shape: appId only.
	req := httptest.NewRequest("POST", "/__mirrorstack/platform/lifecycle/app/install",
		strings.NewReader(`{"appId":"eeeeeeee-5555-5555-5555-555555555555"}`))
	req.Header.Set("X-MS-Internal-Secret", "secret")
	rec := httptest.NewRecorder()
	m.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("bare-body install status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if inSchema("public") {
		t.Errorf("public.%s exists — a schema-less lifecycle call created a per-app table in the shared schema", table)
	}
}

// runtimeAppSchema derives app_<id> the way both the platform and the dev
// middleware do.
func runtimeAppSchema(t *testing.T, appID string) (string, bool) {
	t.Helper()
	return devAppSchemaName(appID)
}

// postInstall drives the real platform lifecycle route with the body shape the
// platform's DEPLOYED transport sends (appId + schema; the credential half is
// covered in system/lifecycle_test.go).
func postInstall(t *testing.T, m *Module, appID, schema string) (int, string) {
	t.Helper()
	body := fmt.Sprintf(`{"appId":%q,"schema":%q}`, appID, schema)
	req := httptest.NewRequest("POST", "/__mirrorstack/platform/lifecycle/app/install", strings.NewReader(body))
	req.Header.Set("X-MS-Internal-Secret", "secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	m.Router().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// postContribution registers one contribution through the mounted /contrib
// route, with the app schema in the request context the way the Lambda shim
// injects it on the deployed plane.
func postContribution(t *testing.T, m *Module, schema, contributorID, payload string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("POST", "/__mirrorstack/contrib/auth-provider/"+contributorID, strings.NewReader(payload))
	req.Header.Set("X-MS-Internal-Secret", "secret")
	req = req.WithContext(db.WithSchema(req.Context(), schema))
	rec := httptest.NewRecorder()
	m.Router().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// listContributions reads the slot through the mounted route and returns the
// contribution ids.
func listContributions(t *testing.T, m *Module, schema string) []string {
	t.Helper()
	req := httptest.NewRequest("GET", "/__mirrorstack/contrib/auth-provider", nil)
	req.Header.Set("X-MS-Internal-Secret", "secret")
	req = req.WithContext(db.WithSchema(req.Context(), schema))
	rec := httptest.NewRecorder()
	m.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Contributions []struct {
			ID string `json:"id"`
		} `json:"contributions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	ids := make([]string, 0, len(resp.Contributions))
	for _, c := range resp.Contributions {
		ids = append(ids, c.ID)
	}
	return ids
}
