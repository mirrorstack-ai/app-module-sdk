package system

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/migration"
)

// noopTxRunner is a TxRunner stub that fails the test if invoked.
//
// Tests in this file use empty (or near-empty) sqlFS specifically so that
// List/Slice return empty slices and Apply/ApplyDown short-circuit BEFORE
// touching the runner. The stub asserts that contract: if a future refactor
// makes Apply call runTx unconditionally (e.g., to pre-warm state outside
// the slice loop), these tests will fail loudly rather than silently
// exercise an unintended SQL path.
//
// For tests that legitimately need to drive the runner (Apply/ApplyDown
// against a real DB), use integration-tagged tests with a real Postgres,
// not this stub.
func noopTxRunner(t *testing.T) migration.TxRunner {
	return func(ctx context.Context, fn func(q db.Querier) error) error {
		t.Helper()
		t.Errorf("TxRunner unexpectedly called — test assumed empty sqlFS would short-circuit before SQL execution")
		return nil
	}
}

func TestInstallHandler_EmptyFS(t *testing.T) {
	t.Parallel()

	h := InstallHandler(nil, migration.ScopeApp, noopTxRunner(t))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/install", nil))

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got LifecycleResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Applied) != 0 {
		t.Errorf("applied = %v, want empty for empty FS", got.Applied)
	}
}

func TestUpgradeHandler_RequiresFromTo(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
	}{
		{"empty body", ""},
		{"missing from", `{"to": "0001"}`},
		{"missing to", `{"from": "0000"}`},
		{"both empty strings", `{"from": "", "to": ""}`},
		{"malformed json", `{from: 0000`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := UpgradeHandler(nil, migration.ScopeApp, noopTxRunner(t))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("POST", "/upgrade", strings.NewReader(tc.body)))

			if rec.Code != 400 {
				t.Errorf("status = %d, want 400 for body %q", rec.Code, tc.body)
			}
		})
	}
}

func TestUpgradeHandler_RejectsSemver(t *testing.T) {
	t.Parallel()

	// The platform is responsible for semver→migration translation before
	// calling the SDK — the handler must refuse non-numeric versions rather
	// than silently pass them through. Error message hints at the fix so a
	// caller who wired the pipe wrong sees it immediately.
	cases := []struct {
		name string
		body string
	}{
		{"semver from", `{"from": "v0.1.0", "to": "0008"}`},
		{"semver to", `{"from": "0000", "to": "v0.1.0"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := UpgradeHandler(nil, migration.ScopeApp, noopTxRunner(t))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("POST", "/upgrade", strings.NewReader(tc.body)))

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 for semver input", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "migration number") {
				t.Errorf("error body = %q, want hint about migration numbers", rec.Body.String())
			}
		})
	}
}

func TestUpgradeHandler_UnknownTarget(t *testing.T) {
	t.Parallel()

	// Empty sqlFS + valid body → Slice fails because the target version
	// doesn't exist in the (empty) migrations list. The handler must
	// translate the runner's error into a 400 (caller asked for something
	// the module doesn't have).
	h := UpgradeHandler(nil, migration.ScopeApp, noopTxRunner(t))
	body := strings.NewReader(`{"from": "0000", "to": "0001"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/upgrade", body))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (unknown target with empty fs)", rec.Code)
	}
}

func TestUpgradeHandler_NoOp(t *testing.T) {
	t.Parallel()

	// from == to (both present in sqlFS) → Slice returns empty → Apply
	// short-circuits before the runner is called → 200 with empty Applied.
	// This exercises the happy-path wiring without needing a real DB.
	fsys := fstest.MapFS{
		"sql/app/0008_a.up.sql": &fstest.MapFile{Data: []byte("")},
	}
	h := UpgradeHandler(fsys, migration.ScopeApp, noopTxRunner(t))
	body := strings.NewReader(`{"from": "0008", "to": "0008"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/upgrade", body))

	if rec.Code != 200 {
		t.Errorf("status = %d, want 200 (no-op upgrade)", rec.Code)
	}
	var got LifecycleResult
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Applied) != 0 {
		t.Errorf("applied = %v, want empty (no-op)", got.Applied)
	}
}

func TestDowngradeHandler_RequiresFromGreaterThanTo(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		"sql/app/0001_a.up.sql":   &fstest.MapFile{Data: []byte("")},
		"sql/app/0001_a.down.sql": &fstest.MapFile{Data: []byte("")},
	}
	h := DowngradeHandler(fsys, migration.ScopeApp, noopTxRunner(t))

	// from=0001, to=0001 → not a downgrade → 400
	body := strings.NewReader(`{"from": "0001", "to": "0001"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/downgrade", body))

	if rec.Code != 400 {
		t.Errorf("status = %d, want 400 for from <= to", rec.Code)
	}
}

func TestUninstallHandler_AlwaysSucceeds(t *testing.T) {
	t.Parallel()

	h := UninstallHandler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/uninstall", nil))

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got UninstallResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != "ok" {
		t.Errorf("status = %q, want ok", got.Status)
	}
}

func TestUpgradeHandler_ModuleScope(t *testing.T) {
	t.Parallel()

	// Same shape as TestUpgradeHandler_NoOp but for the module scope.
	// Pins that the scope parameter is wired all the way through:
	// the handler reads sql/module/ when scope=ScopeModule, NOT sql/app/.
	// If the wiring is wrong, ScopeModule would read sql/app/0008_a.up.sql,
	// find the version, and the test still passes — so the fixture has
	// 0008 ONLY in sql/module/ to make a misrouted read fail with
	// "unknown target version 0008".
	fsys := fstest.MapFS{
		"sql/module/0008_a.up.sql": &fstest.MapFile{Data: []byte("")},
	}
	h := UpgradeHandler(fsys, migration.ScopeModule, noopTxRunner(t))
	body := strings.NewReader(`{"from": "0008", "to": "0008"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/upgrade", body))

	if rec.Code != 200 {
		t.Errorf("status = %d, want 200 (no-op upgrade on module scope)", rec.Code)
	}
}

func TestInstallHandler_BadFS_500(t *testing.T) {
	t.Parallel()

	// fs.FS that returns an error on every Open. fs.ReadDir wraps and
	// surfaces the error — the handler should respond 500.
	h := InstallHandler(errFS{}, migration.ScopeApp, noopTxRunner(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/install", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 for fs read error", rec.Code)
	}
}

// errFS implements fs.FS and returns a non-NotExist error on every Open.
// fs.ErrInvalid is treated as a real error (not the "missing sql/ dir"
// special case the runner handles internally).
type errFS struct{}

func (errFS) Open(name string) (fs.File, error) {
	return nil, fs.ErrInvalid
}

// capturingTxRunner records the context it was invoked with so tests can
// assert what credentials/schema flowed through from the request body.
// Pair with oneMigrationFS so Apply invokes the runner exactly once.
func capturingTxRunner(t *testing.T, gotCtx *context.Context) migration.TxRunner {
	return func(ctx context.Context, fn func(q db.Querier) error) error {
		t.Helper()
		*gotCtx = ctx
		return nil
	}
}

// oneMigrationFS returns an fs.FS with a single empty .up.sql so Apply
// invokes the runner exactly once without running real SQL.
func oneMigrationFS() fs.FS {
	return fstest.MapFS{
		"sql/app/0001_init.up.sql": {Data: []byte("")},
	}
}

func TestInstallHandler_BodyInjectsCredentialAndSchema(t *testing.T) {
	// Not t.Parallel because t.Setenv on MS_LOCAL_DB_URL would race with
	// concurrent tests reading the env base.

	// Point the env base at a known URL so we can assert host/port/database
	// come from the environment, not the body.
	t.Setenv("MS_LOCAL_DB_URL", "postgres://envuser:envpw@db.platform.local:6543/platform_apps?sslmode=disable")

	var captured context.Context
	h := InstallHandler(oneMigrationFS(), migration.ScopeApp, capturingTxRunner(t, &captured))

	body := strings.NewReader(`{
		"appId":  "6c8d1234-abcd-ef01-2345-6789abcdef00",
		"schema": "app_6c8d1234_abcd_ef01_2345_6789abcdef00",
		"credential": {
			"username": "r_6c8d1234_oauth-core",
			"token":    "secret-token"
		}
	}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/install", body))

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if captured == nil {
		t.Fatal("runner never invoked — Apply short-circuited unexpectedly")
	}
	if got := db.SchemaFrom(captured); got != "app_6c8d1234_abcd_ef01_2345_6789abcdef00" {
		t.Errorf("SchemaFrom = %q, want app_6c8d1234_...", got)
	}
	cred := db.CredentialFrom(captured)
	if cred == nil {
		t.Fatal("CredentialFrom returned nil; expected per-module credential")
	}
	// Per-install fields from the body.
	if cred.Username != "r_6c8d1234_oauth-core" || cred.Token != "secret-token" {
		t.Errorf("credential per-install = username=%q token=%q, want r_6c8d1234_oauth-core / secret-token", cred.Username, cred.Token)
	}
	// Static fields from the env URL — the body does NOT carry these any more.
	if cred.Host != "db.platform.local" || cred.Port != 6543 || cred.Database != "platform_apps" {
		t.Errorf("credential env-base = host=%q port=%d db=%q, want db.platform.local/6543/platform_apps", cred.Host, cred.Port, cred.Database)
	}
}

func TestInstallHandler_EmptyBodyFallsThrough(t *testing.T) {
	t.Parallel()

	// Dev-mount migration auto-apply posts no body. Handler must succeed and
	// leave ctx without injected schema or credential so resolvePoolFor
	// falls back to the dev pool.
	var captured context.Context
	h := InstallHandler(oneMigrationFS(), migration.ScopeApp, capturingTxRunner(t, &captured))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/install", nil))

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if db.SchemaFrom(captured) != "" {
		t.Errorf("SchemaFrom = %q, want empty for body-less request", db.SchemaFrom(captured))
	}
	if db.CredentialFrom(captured) != nil {
		t.Errorf("CredentialFrom = %+v, want nil for body-less request", db.CredentialFrom(captured))
	}
}

func TestInstallHandler_MalformedBody_400(t *testing.T) {
	t.Parallel()

	h := InstallHandler(nil, migration.ScopeApp, noopTxRunner(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/install", strings.NewReader(`{not json`)))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for malformed body", rec.Code)
	}
}

func TestUpgradeHandler_BodyInjectsCredentialAndSchema(t *testing.T) {
	// Not t.Parallel because t.Setenv on MS_LOCAL_DB_URL would race with
	// concurrent tests reading the env base.

	// Point the env base at a known URL so we can assert host/port/database
	// come from the environment, not the body — the exact contract install
	// already pins (see TestInstallHandler_BodyInjectsCredentialAndSchema).
	t.Setenv("MS_LOCAL_DB_URL", "postgres://envuser:envpw@db.platform.local:6543/platform_apps?sslmode=disable")

	var captured context.Context
	h := UpgradeHandler(oneMigrationFS(), migration.ScopeApp, capturingTxRunner(t, &captured))

	body := strings.NewReader(`{
		"from":   "0000",
		"to":     "0001",
		"appId":  "6c8d1234-abcd-ef01-2345-6789abcdef00",
		"schema": "app_6c8d1234_abcd_ef01_2345_6789abcdef00",
		"credential": {
			"username": "r_6c8d1234_oauth-core",
			"token":    "secret-token"
		}
	}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/upgrade", body))

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if captured == nil {
		t.Fatal("runner never invoked — Apply short-circuited unexpectedly")
	}
	if got := db.SchemaFrom(captured); got != "app_6c8d1234_abcd_ef01_2345_6789abcdef00" {
		t.Errorf("SchemaFrom = %q, want app_6c8d1234_...", got)
	}
	cred := db.CredentialFrom(captured)
	if cred == nil {
		t.Fatal("CredentialFrom returned nil; expected per-(app, module) credential")
	}
	if cred.Username != "r_6c8d1234_oauth-core" || cred.Token != "secret-token" {
		t.Errorf("credential per-install = username=%q token=%q, want r_6c8d1234_oauth-core / secret-token", cred.Username, cred.Token)
	}
	if cred.Host != "db.platform.local" || cred.Port != 6543 || cred.Database != "platform_apps" {
		t.Errorf("credential env-base = host=%q port=%d db=%q, want db.platform.local/6543/platform_apps", cred.Host, cred.Port, cred.Database)
	}
}

func TestUpgradeHandler_FromToOnlyKeepsDevPath(t *testing.T) {
	t.Parallel()

	// The dev-tunnel path posts only {from, to}. The handler must succeed
	// and leave ctx without schema or credential so resolvePoolFor falls
	// back to the dev pool — pre-existing behavior, unchanged.
	var captured context.Context
	h := UpgradeHandler(oneMigrationFS(), migration.ScopeApp, capturingTxRunner(t, &captured))
	body := strings.NewReader(`{"from": "0000", "to": "0001"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/upgrade", body))

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if captured == nil {
		t.Fatal("runner never invoked — Apply short-circuited unexpectedly")
	}
	if got := db.SchemaFrom(captured); got != "" {
		t.Errorf("SchemaFrom = %q, want empty for from/to-only body", got)
	}
	if db.CredentialFrom(captured) != nil {
		t.Errorf("CredentialFrom = %+v, want nil for from/to-only body", db.CredentialFrom(captured))
	}
}

func TestUpgradeHandler_MalformedCredential_400(t *testing.T) {
	t.Parallel()

	// A credential of the wrong JSON type must be a clean 400 from the body
	// decoder — never a panic, never a silent env-pool fallback.
	cases := []struct {
		name string
		body string
	}{
		{"credential wrong type", `{"from": "0000", "to": "0001", "credential": "nope"}`},
		{"credential array", `{"from": "0000", "to": "0001", "credential": ["u", "t"]}`},
		{"schema wrong type", `{"from": "0000", "to": "0001", "schema": 42}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := UpgradeHandler(oneMigrationFS(), migration.ScopeApp, noopTxRunner(t))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("POST", "/upgrade", strings.NewReader(tc.body)))

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 for body %q", rec.Code, tc.body)
			}
		})
	}
}

func TestUpgradeHandler_EmptyCredentialObjectFallsThrough(t *testing.T) {
	t.Parallel()

	// `"credential": {}` (shape compat) must behave like no credential at
	// all — same rule injectInstallContext applies on install.
	var captured context.Context
	h := UpgradeHandler(oneMigrationFS(), migration.ScopeApp, capturingTxRunner(t, &captured))
	body := strings.NewReader(`{"from": "0000", "to": "0001", "credential": {}}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/upgrade", body))

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if db.CredentialFrom(captured) != nil {
		t.Error("CredentialFrom should be nil for an empty credential object")
	}
}

func TestDowngradeHandler_BodyInjectsCredentialAndSchema(t *testing.T) {
	// Not t.Parallel — t.Setenv (see the upgrade variant).

	// Downgrade shares decodeUpgradeRequest + injectLifecycleContext with
	// upgrade; pin that the credential context flows into ApplyDown too.
	t.Setenv("MS_LOCAL_DB_URL", "postgres://envuser:envpw@db.platform.local:6543/platform_apps?sslmode=disable")

	fsys := fstest.MapFS{
		"sql/app/0001_init.up.sql":   &fstest.MapFile{Data: []byte("")},
		"sql/app/0001_init.down.sql": &fstest.MapFile{Data: []byte("")},
	}
	var captured context.Context
	h := DowngradeHandler(fsys, migration.ScopeApp, capturingTxRunner(t, &captured))
	body := strings.NewReader(`{
		"from":   "0001",
		"to":     "0000",
		"schema": "app_6c8d1234_abcd_ef01_2345_6789abcdef00",
		"credential": {"username": "r_6c8d1234_oauth-core", "token": "secret-token"}
	}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/downgrade", body))

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if captured == nil {
		t.Fatal("runner never invoked — ApplyDown short-circuited unexpectedly")
	}
	if got := db.SchemaFrom(captured); got != "app_6c8d1234_abcd_ef01_2345_6789abcdef00" {
		t.Errorf("SchemaFrom = %q, want app_6c8d1234_...", got)
	}
	cred := db.CredentialFrom(captured)
	if cred == nil || cred.Username != "r_6c8d1234_oauth-core" || cred.Token != "secret-token" {
		t.Errorf("CredentialFrom = %+v, want r_6c8d1234_oauth-core / secret-token", cred)
	}
}

func TestInstallHandler_PartialBodyOmitsMissingFields(t *testing.T) {
	t.Parallel()

	var captured context.Context
	h := InstallHandler(oneMigrationFS(), migration.ScopeApp, capturingTxRunner(t, &captured))
	body := strings.NewReader(`{"schema": "app_only"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/install", body))

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := db.SchemaFrom(captured); got != "app_only" {
		t.Errorf("SchemaFrom = %q, want app_only", got)
	}
	if db.CredentialFrom(captured) != nil {
		t.Error("CredentialFrom should be nil when body omits credential")
	}
}
