package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"
	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/migration"
	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
	p "github.com/mirrorstack-ai/app-module-sdk/roles"
	"github.com/mirrorstack-ai/app-module-sdk/system"
)

func resetDefault(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { defaultModule = nil })
	defaultModule = nil
}

// newTestModuleWithSecret creates a Module with MS_INTERNAL_SECRET set to
// "secret" — the canonical setup for tests that exercise internal-scope
// routes (manifest, lifecycle, events, crons). Use the lowercase id for
// stable manifest assertions.
//
// IMPORTANT: t.Setenv MUST run before New(), which is why this helper
// bundles them. Module.New() captures auth.InternalAuth() at construction;
// the cached middleware closure reads MS_INTERNAL_SECRET once and never
// re-reads. A test that calls New() then sets the env afterward will
// silently produce a module with the wrong secret and fail with
// confusing 401/503 responses.
func newTestModuleWithSecret(t *testing.T, id string) *Module {
	t.Helper()
	t.Setenv("MS_INTERNAL_SECRET", "secret")
	m, err := New(Config{ID: id})
	if err != nil {
		t.Fatalf("New(%q): %v", id, err)
	}
	return m
}

// assertPanics runs fn and fails the test if fn does not panic. msg is the
// error message used when no panic occurred. Mirrors the recover-pattern
// previously duplicated across event_test.go, cron_test.go,
// permission_test.go, and registry_test.go.
func assertPanics(t *testing.T, msg string, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r == nil {
			t.Error(msg)
		}
	}()
	fn()
}

func doRequest(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func doRequestWithRole(t *testing.T, h http.Handler, method, path, role string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if role != "" {
		req = req.WithContext(auth.Set(req.Context(), auth.Identity{AppRole: role}))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func doRequestWithSecret(t *testing.T, h http.Handler, method, path, secret string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if secret != "" {
		req.Header.Set("X-MS-Internal-Secret", secret)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// --- Struct API (New) ---

func TestNew(t *testing.T) {
	m, err := New(Config{ID: "media", Name: "Media", Icon: "perm_media"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Config().ID != "media" {
		t.Errorf("expected ID 'media', got %q", m.Config().ID)
	}
	if m.Config().Name != "Media" {
		t.Errorf("expected Name 'Media', got %q", m.Config().Name)
	}
	if m.Config().Icon != "perm_media" {
		t.Errorf("expected Icon 'perm_media', got %q", m.Config().Icon)
	}
}

func TestNew_EmptyID(t *testing.T) {
	_, err := New(Config{})
	if err == nil {
		t.Error("expected error for empty ID")
	}
}

func TestNew_RejectsBadID(t *testing.T) {
	bad := []string{
		"Media",                                 // uppercase
		"media!",                                // special char
		"1media",                                // starts with digit
		"_media",                                // starts with underscore
		"../etc",                                // path traversal
		"abcdefghijklmnopqrstuvwxyz0123456789a", // 37 chars — one over the 36-char ceiling
	}
	for _, id := range bad {
		_, err := New(Config{ID: id})
		if err == nil {
			t.Errorf("expected error for ID %q", id)
		}
	}
}

func TestNew_AcceptsValidID(t *testing.T) {
	good := []string{
		"media",
		"oauth",
		"billing_engine",
		"v2_oauth",
		// UUID-derived shape the CLI scaffold emits: "m" + 32 hex chars = 33.
		"m0123456789abcdef0123456789abcdef0",
		// 36-char boundary (max accepted).
		"abcdefghijklmnopqrstuvwxyz0123456789",
	}
	for _, id := range good {
		_, err := New(Config{ID: id})
		if err != nil {
			t.Errorf("unexpected error for ID %q: %v", id, err)
		}
	}
}

func TestNew_AcceptsEmptySlug(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{ID: "media"}); err != nil {
		t.Errorf("expected empty Slug to be accepted in dev mode, got %v", err)
	}
}

func TestNew_AcceptsValidSlug(t *testing.T) {
	t.Parallel()
	good := []string{
		"oauth",
		"oauth-v2",
		"a",
		"abcdefghijklmnop", // 16-char boundary
		"x1",
		"my-app-store",
	}
	for _, slug := range good {
		t.Run(slug, func(t *testing.T) {
			if _, err := New(Config{ID: "media", Slug: slug}); err != nil {
				t.Errorf("unexpected error for Slug %q: %v", slug, err)
			}
		})
	}
}

func TestNew_RejectsBadSlug(t *testing.T) {
	t.Parallel()
	bad := []string{
		"OAuth",             // uppercase
		"oauth_v2",          // underscore (slugs are kebab-case)
		"-oauth",            // starts with hyphen
		"1oauth",            // starts with digit
		"oauth.v2",          // dot
		"oauth v2",          // space
		"abcdefghijklmnopq", // 17 chars — one over
	}
	for _, slug := range bad {
		t.Run(slug, func(t *testing.T) {
			if _, err := New(Config{ID: "media", Slug: slug}); err == nil {
				t.Errorf("expected error for Slug %q", slug)
			}
		})
	}
}

func TestRouter(t *testing.T) {
	m, err := New(Config{ID: "test", Name: "Test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m.Router().Get("/ping", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("pong"))
	})

	rec := doRequest(t, m.Router(), "GET", "/ping")
	if rec.Code != 200 {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != "pong" {
		t.Errorf("expected 'pong', got %q", rec.Body.String())
	}
}

// --- Scope auth enforcement ---

func TestPlatform_LocalDevBypass_InjectsSyntheticAdmin(t *testing.T) {
	// Local dev + no MS_INTERNAL_SECRET: PlatformAuth injects a synthetic
	// admin identity so `mirrorstack dev` (no tunnel) can render
	// platform-scope routes without the developer wiring auth. Tunnel
	// mode flips this branch off by setting the secret (see
	// auth.platformAuth doc-matrix + mirrorstack-cli #33).
	t.Setenv("MS_INTERNAL_SECRET", "")
	m, _ := New(Config{ID: "test", Name: "Test"})
	m.Platform(func(r chi.Router) {
		r.Get("/admin", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("admin"))
		})
	})

	rec := doRequest(t, m.Router(), "GET", "/platform/admin")
	if rec.Code != 200 {
		t.Errorf("expected 200 (synthetic admin injected), got %d", rec.Code)
	}
}

func TestPlatform_SecretSet_RejectsNoHeader(t *testing.T) {
	// When MS_INTERNAL_SECRET is set (tunnel mode or prod), local bypass
	// is off — a Platform-scope route 401s without a valid trusted-
	// forwarder header.
	t.Setenv("MS_INTERNAL_SECRET", "real-secret")
	m, _ := New(Config{ID: "test", Name: "Test"})
	m.Platform(func(r chi.Router) {
		r.Get("/admin", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("admin"))
		})
	})

	rec := doRequest(t, m.Router(), "GET", "/platform/admin")
	if rec.Code != 401 {
		t.Errorf("expected 401 without trusted-forwarder header, got %d", rec.Code)
	}
}

func TestPlatform_AcceptsAnyRole(t *testing.T) {
	m, _ := New(Config{ID: "test", Name: "Test"})
	m.Platform(func(r chi.Router) {
		r.Get("/dashboard", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("ok"))
		})
	})

	// PlatformAuth checks authentication only — any role passes
	// Use RequirePermission for authorization (which roles)
	for _, role := range []string{auth.RoleAdmin, auth.RoleMember, auth.RoleViewer} {
		rec := doRequestWithRole(t, m.Router(), "GET", "/platform/dashboard", role)
		if rec.Code != 200 {
			t.Errorf("role %q: expected 200, got %d", role, rec.Code)
		}
	}
}

func TestPublic_AcceptsAnonymous(t *testing.T) {
	m, _ := New(Config{ID: "test", Name: "Test"})
	m.Public(func(r chi.Router) {
		r.Get("/items", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("public"))
		})
	})

	rec := doRequest(t, m.Router(), "GET", "/public/items")
	if rec.Code != 200 {
		t.Errorf("expected 200 for anonymous, got %d", rec.Code)
	}
}

func TestInternal_RejectsNoSecret(t *testing.T) {
	m := newTestModuleWithSecret(t, "test")
	m.Internal(func(r chi.Router) {
		r.Post("/event", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("internal"))
		})
	})

	rec := doRequest(t, m.Router(), "POST", "/internal/event")
	if rec.Code != 401 {
		t.Errorf("expected 401 without secret, got %d", rec.Code)
	}
}

func TestInternal_AcceptsValidSecret(t *testing.T) {
	m := newTestModuleWithSecret(t, "test")
	m.Internal(func(r chi.Router) {
		r.Post("/event", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("internal"))
		})
	})

	rec := doRequestWithSecret(t, m.Router(), "POST", "/internal/event", "secret")
	if rec.Code != 200 {
		t.Errorf("expected 200 with valid secret, got %d", rec.Code)
	}
}

// --- Permission middleware ---

func TestRequirePermission_AllowsMember(t *testing.T) {
	t.Parallel() // safe now that permission state lives on the Module instance, not auth package globals

	m, _ := New(Config{ID: "test", Name: "Test"})
	m.RegisterPermission("media.view", PermissionOpts{DefaultRole: p.Viewer(), CustomRoles: []string{"member"}})
	m.Platform(func(r chi.Router) {
		r.With(m.RequirePermission("media.view")).Get("/items", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("ok"))
		})
	})

	rec := doRequestWithRole(t, m.Router(), "GET", "/platform/items", auth.RoleMember)
	if rec.Code != 200 {
		t.Errorf("expected 200 for member with media.view, got %d", rec.Code)
	}
}

func TestRequirePermission_RejectsViewer(t *testing.T) {
	t.Parallel()

	m, _ := New(Config{ID: "test", Name: "Test"})
	m.RegisterPermission("media.delete", PermissionOpts{DefaultRole: p.Admin()})
	m.Platform(func(r chi.Router) {
		r.With(m.RequirePermission("media.delete")).Delete("/items/{id}", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("deleted"))
		})
	})

	rec := doRequestWithRole(t, m.Router(), "DELETE", "/platform/items/123", auth.RoleViewer)
	if rec.Code != 403 {
		t.Errorf("expected 403 for viewer on admin-only permission, got %d", rec.Code)
	}
}

func TestRequirePermission_AppearsInManifest(t *testing.T) {
	// Permissions registered via Module.RequirePermission must show up in the
	// manifest payload — that's the whole point of consolidating into the
	// per-Module registry. Two parallel modules in the same process must NOT
	// see each other's permissions (the package-global registry would have).
	t.Setenv("MS_INTERNAL_SECRET", "secret")
	m1, _ := New(Config{ID: "media", Name: "Media"})
	m2, _ := New(Config{ID: "video", Name: "Video"})

	m1.RegisterPermission("media.upload", PermissionOpts{DefaultRole: p.Admin(), CustomRoles: []string{"member"}})
	m1.RegisterPermission("media.view", PermissionOpts{DefaultRole: p.Viewer(), CustomRoles: []string{"member"}})
	m1.Platform(func(r chi.Router) {
		r.With(m1.RequirePermission("media.upload")).Post("/upload", func(w http.ResponseWriter, r *http.Request) {})
		r.With(m1.RequirePermission("media.view")).Get("/items", func(w http.ResponseWriter, r *http.Request) {})
	})
	m2.RegisterPermission("video.transcode", PermissionOpts{DefaultRole: p.Admin()})
	m2.Platform(func(r chi.Router) {
		r.With(m2.RequirePermission("video.transcode")).Post("/transcode", func(w http.ResponseWriter, r *http.Request) {})
	})

	rec := doRequestWithSecret(t, m1.Router(), "GET", "/__mirrorstack/platform/manifest", "secret")
	var got system.ManifestPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	if len(got.Permissions) != 2 {
		t.Fatalf("m1 manifest permissions = %d, want 2 (media.upload, media.view): %+v", len(got.Permissions), got.Permissions)
	}
	for _, p := range got.Permissions {
		if p.Name == "video.transcode" {
			t.Errorf("m1 manifest leaked permission from m2: %+v", p)
		}
	}
}

// --- Auto-prefix behavior ---

// fetchManifest hits the system /__mirrorstack/platform/manifest endpoint
// with the test secret and decodes the response. Shared by the permission
// auto-prefix tests so each test asserts on the manifest shape directly.
func fetchManifest(t *testing.T, m *Module) system.ManifestPayload {
	t.Helper()
	rec := doRequestWithSecret(t, m.Router(), "GET", "/__mirrorstack/platform/manifest", "secret")
	var got system.ManifestPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	return got
}

func TestRequirePermission_AutoPrefix(t *testing.T) {
	cases := []struct {
		name       string
		slug       string
		permission string
		want       string
	}{
		{"adds slug prefix", "oauth-core", "users.view", "oauth-core.users.view"},
		{"preserves already-qualified name", "oauth-core", "oauth-core.users.view", "oauth-core.users.view"},
		{"slug-less module skips prefix", "", "media.view", "media.view"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MS_INTERNAL_SECRET", "secret")
			m, _ := New(Config{ID: "test", Slug: tc.slug, Name: "Test"})
			m.RegisterPermission(tc.permission, PermissionOpts{DefaultRole: p.Admin()})
			m.Platform(func(r chi.Router) {
				r.With(m.RequirePermission(tc.permission)).Get("/x", func(w http.ResponseWriter, r *http.Request) {})
			})

			got := fetchManifest(t, m)
			if len(got.Permissions) != 1 || got.Permissions[0].Name != tc.want {
				t.Errorf("permissions = %+v, want one entry %q", got.Permissions, tc.want)
			}
		})
	}
}

// TestScopes_AutoMountUnderPrefix asserts each scope's routes resolve under
// the conventional /<scope>/ prefix AND that the bare path 404s — so a
// future regression that drops the wrapping doesn't pass silently.
func TestScopes_AutoMountUnderPrefix(t *testing.T) {
	cases := []struct {
		scope     registry.Scope
		method    string
		suffix    string
		needsAuth bool // Internal scope needs MS_INTERNAL_SECRET to reach the handler
	}{
		{registry.ScopePlatform, "GET", "/users", false}, // local-dev bypass injects synthetic admin
		{registry.ScopePublic, "GET", "/me", false},
		{registry.ScopeInternal, "POST", "/sessions", true},
	}
	for _, tc := range cases {
		t.Run(string(tc.scope), func(t *testing.T) {
			var m *Module
			if tc.needsAuth {
				m = newTestModuleWithSecret(t, "test")
			} else {
				m, _ = New(Config{ID: "test", Name: "Test"})
			}
			mount := map[registry.Scope]func(func(chi.Router)){
				registry.ScopePlatform: m.Platform,
				registry.ScopePublic:   m.Public,
				registry.ScopeInternal: m.Internal,
			}[tc.scope]
			mount(func(r chi.Router) {
				r.Method(tc.method, tc.suffix, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = w.Write([]byte("ok"))
				}))
			})

			prefixed := "/" + string(tc.scope) + tc.suffix
			req := func(path string) int {
				if tc.needsAuth {
					return doRequestWithSecret(t, m.Router(), tc.method, path, "secret").Code
				}
				return doRequest(t, m.Router(), tc.method, path).Code
			}
			if got := req(prefixed); got != 200 {
				t.Errorf("prefixed %s: got %d, want 200", prefixed, got)
			}
			if got := req(tc.suffix); got != 404 {
				t.Errorf("bare %s must NOT resolve: got %d, want 404", tc.suffix, got)
			}
		})
	}
}

// --- System routes ---

func TestHealthAutoMounted(t *testing.T) {
	m, _ := New(Config{ID: "test", Name: "Test"})
	rec := doRequest(t, m.Router(), "GET", "/__mirrorstack/health")
	if rec.Code != 200 {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestSystemPlatformRoutes_RequireInternalSecret(t *testing.T) {
	m := newTestModuleWithSecret(t, "test")

	// Without secret → 401. Asserting exactly 401 (not just !=200) so an
	// accidental route removal — which would return 404 — fails this test
	// instead of providing false assurance about the auth boundary.
	rec := doRequest(t, m.Router(), "GET", "/__mirrorstack/platform/manifest")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without secret, got %d", rec.Code)
	}
}

// --- Manifest endpoint ---

func TestManifest_Returns200WithSecret(t *testing.T) {
	t.Setenv("MS_INTERNAL_SECRET", "secret")
	m, _ := New(Config{ID: "media", Name: "Media", Icon: "perm_media"})

	rec := doRequestWithSecret(t, m.Router(), "GET", "/__mirrorstack/platform/manifest", "secret")
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var got system.ManifestPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if got.ID != "media" || got.Defaults.Name != "Media" || got.Defaults.Icon != "perm_media" {
		t.Errorf("manifest identity wrong: %+v", got)
	}
}

func TestManifest_RoutesFromAllScopes_RegisteredViaModule(t *testing.T) {
	m := newTestModuleWithSecret(t, "media")

	m.Platform(func(r chi.Router) {
		r.Get("/items", func(w http.ResponseWriter, r *http.Request) {})
		r.Post("/items", func(w http.ResponseWriter, r *http.Request) {})
	})
	m.Public(func(r chi.Router) {
		r.Get("/feed", func(w http.ResponseWriter, r *http.Request) {})
	})
	m.Internal(func(r chi.Router) {
		r.Post("/events/on-user-deleted", func(w http.ResponseWriter, r *http.Request) {})
	})

	rec := doRequestWithSecret(t, m.Router(), "GET", "/__mirrorstack/platform/manifest", "secret")
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got system.ManifestPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	if len(got.Routes[registry.ScopePlatform]) != 2 {
		t.Errorf("platform routes = %d, want 2: %v", len(got.Routes[registry.ScopePlatform]), got.Routes[registry.ScopePlatform])
	}
	if len(got.Routes[registry.ScopePublic]) != 1 {
		t.Errorf("public routes = %d, want 1: %v", len(got.Routes[registry.ScopePublic]), got.Routes[registry.ScopePublic])
	}
	if len(got.Routes[registry.ScopeInternal]) != 1 {
		t.Errorf("internal routes = %d, want 1: %v", len(got.Routes[registry.ScopeInternal]), got.Routes[registry.ScopeInternal])
	}
}

func TestManifest_MigrationFromConfig(t *testing.T) {
	t.Setenv("MS_INTERNAL_SECRET", "secret")
	sqlFS := fstest.MapFS{
		"sql/app/0000_initial.up.sql":   &fstest.MapFile{Data: []byte("")},
		"sql/app/0008_add_index.up.sql": &fstest.MapFile{Data: []byte("")},
	}
	m, _ := New(Config{ID: "media", Name: "Media", SQL: sqlFS})

	rec := doRequestWithSecret(t, m.Router(), "GET", "/__mirrorstack/platform/manifest", "secret")
	var got system.ManifestPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	if got.Migration.App != "0008" {
		t.Errorf("migration.app = %q, want 0008", got.Migration.App)
	}
}

// --- Lifecycle endpoints ---
//
// Each test below covers BOTH the /lifecycle/app/* and /lifecycle/module/*
// scope namespaces. The behavior is identical across scopes — only the
// migration directory the handler reads from changes — so a single table
// drives both. A regression that mounts only one scope, or that wires the
// wrong handler under one of the namespaces, fails the loop here. Iteration
// is keyed off migration.AllScopes() so a future scope addition is picked
// up automatically.

func TestLifecycle_RoutesRequireInternalSecret(t *testing.T) {
	m := newTestModuleWithSecret(t, "test")

	for _, scope := range migration.AllScopes() {
		for _, action := range []string{"install", "upgrade", "downgrade", "uninstall"} {
			route := "/__mirrorstack/platform/lifecycle/" + string(scope) + "/" + action
			t.Run(route, func(t *testing.T) {
				rec := doRequest(t, m.Router(), "POST", route)
				if rec.Code != http.StatusUnauthorized {
					t.Errorf("status = %d, want 401", rec.Code)
				}
			})
		}
	}
}

func TestLifecycle_UninstallReturnsOK(t *testing.T) {
	m := newTestModuleWithSecret(t, "test")

	for _, scope := range migration.AllScopes() {
		t.Run(string(scope), func(t *testing.T) {
			rec := doRequestWithSecret(t, m.Router(), "POST", "/__mirrorstack/platform/lifecycle/"+string(scope)+"/uninstall", "secret")
			if rec.Code != 200 {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
				t.Errorf("body = %s, want status:ok", rec.Body.String())
			}
		})
	}
}

func TestLifecycle_InstallEmptyFSReturnsOK(t *testing.T) {
	// No SQL configured → install is a no-op (no migrations to apply).
	m := newTestModuleWithSecret(t, "test")

	for _, scope := range migration.AllScopes() {
		t.Run(string(scope), func(t *testing.T) {
			rec := doRequestWithSecret(t, m.Router(), "POST", "/__mirrorstack/platform/lifecycle/"+string(scope)+"/install", "secret")
			if rec.Code != 200 {
				t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestLifecycle_UpgradeRequiresPayload(t *testing.T) {
	m := newTestModuleWithSecret(t, "test")

	for _, scope := range migration.AllScopes() {
		t.Run(string(scope), func(t *testing.T) {
			// No body → 400
			req := httptest.NewRequest("POST", "/__mirrorstack/platform/lifecycle/"+string(scope)+"/upgrade", nil)
			req.Header.Set("X-MS-Internal-Secret", "secret")
			rec := httptest.NewRecorder()
			m.Router().ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
}

// TestLifecycle_ScopeTxRunnerWiring verifies each scope's lifecycle handlers
// read their own credential context key. An empty Token makes PoolCache
// validate() fail before any dial, so the test stays hermetic and the error
// body echoes the sentinel username only when the correct key was consulted.
func TestLifecycle_ScopeTxRunnerWiring(t *testing.T) {
	t.Setenv("MS_INTERNAL_SECRET", "secret")

	cases := []struct {
		scope    migration.Scope
		sentinel string
		inject   func(context.Context, db.Credential) context.Context
	}{
		{migration.ScopeApp, "app-scope-sentinel-user", db.WithCredential},
		{migration.ScopeModule, "mod-scope-sentinel-user", db.WithModuleCredential},
	}

	for _, tc := range cases {
		t.Run(string(tc.scope), func(t *testing.T) {
			t.Parallel()

			m, err := New(Config{
				ID: "test",
				SQL: fstest.MapFS{
					tc.scope.Dir() + "/0001_probe.up.sql": &fstest.MapFile{Data: []byte("SELECT 1")},
				},
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			cred := db.Credential{
				Host:     "h",
				Port:     5432,
				Database: "d",
				Username: tc.sentinel,
				// Token intentionally empty — PoolCache validate() fails
				// fast, no dial attempted.
			}
			ctx := tc.inject(context.Background(), cred)

			route := "/__mirrorstack/platform/lifecycle/" + string(tc.scope) + "/install"
			req := httptest.NewRequest("POST", route, nil).WithContext(ctx)
			req.Header.Set("X-MS-Internal-Secret", "secret")
			rec := httptest.NewRecorder()
			m.Router().ServeHTTP(rec, req)

			if !strings.Contains(rec.Body.String(), tc.sentinel) {
				t.Errorf(
					"%s should drive migrations via the %s credential, but the "+
						"response body does not mention sentinel user %q.\n"+
						"Status: %d\nBody: %s",
					route, tc.scope, tc.sentinel, rec.Code, rec.Body.String(),
				)
			}
		})
	}
}

func TestManifest_RegisteredScopesStillRouteCorrectly(t *testing.T) {
	// Verify that the chi.Walk + re-register approach in scopedRoutes preserves
	// the original routing behavior. Routes registered via Platform/Public/Internal
	// must still be reachable AND still enforce auth.
	m := newTestModuleWithSecret(t, "test")

	m.Platform(func(r chi.Router) {
		r.Get("/admin/items", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("admin"))
		})
	})
	m.Public(func(r chi.Router) {
		r.Get("/feed", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("public"))
		})
	})

	// Public route — no auth needed
	if rec := doRequest(t, m.Router(), "GET", "/public/feed"); rec.Code != 200 {
		t.Errorf("public route: code = %d, want 200", rec.Code)
	}
	// Platform route without auth → 401
	if rec := doRequest(t, m.Router(), "GET", "/platform/admin/items"); rec.Code != 401 {
		t.Errorf("platform route without auth: code = %d, want 401", rec.Code)
	}
	// Platform route with auth → 200
	if rec := doRequestWithRole(t, m.Router(), "GET", "/platform/admin/items", auth.RoleAdmin); rec.Code != 200 {
		t.Errorf("platform route with admin: code = %d, want 200", rec.Code)
	}
}

// --- Convenience API (Init) ---

func TestInit(t *testing.T) {
	resetDefault(t)
	if err := Init(Config{ID: "media", Name: "Media"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if DefaultModule() == nil {
		t.Error("expected defaultModule to be set")
	}
}

func TestInit_EmptyID(t *testing.T) {
	resetDefault(t)
	if err := Init(Config{}); err == nil {
		t.Error("expected error for empty ID")
	}
}

func TestStart_BeforeInit(t *testing.T) {
	resetDefault(t)
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic")
		}
	}()
	_ = Start()
}

func TestRequireInternalSecret(t *testing.T) {
	// Pulled out as its own helper because Module.Start() in Lambda mode
	// calls lambda.Start() which we cannot drive from a unit test. The
	// helper is the only piece of fail-fast logic we own; once it returns
	// nil, Start() hands off to the AWS Lambda runtime.
	t.Run("missing", func(t *testing.T) {
		t.Setenv("MS_INTERNAL_SECRET", "")
		if err := requireInternalSecret(); err == nil {
			t.Error("expected error when MS_INTERNAL_SECRET is unset")
		}
	})
	t.Run("present", func(t *testing.T) {
		t.Setenv("MS_INTERNAL_SECRET", "any-non-empty-value")
		if err := requireInternalSecret(); err != nil {
			t.Errorf("expected nil error when secret set, got %v", err)
		}
	})
}

func TestPlatformRoutes_MaxBytesLimit(t *testing.T) {
	t.Setenv("MS_INTERNAL_SECRET", "secret")
	m, _ := New(Config{ID: "media", Name: "Media", Icon: "perm_media"})

	// build valid JSON > 64 KB — json.Decode reads it all before failing, triggering MaxBytesReader
	padding := strings.Repeat("a", 64*1024)
	bigJSON := `{"from":"` + padding + `","to":"0001"}`
	req := httptest.NewRequest("POST", "/__mirrorstack/platform/lifecycle/app/upgrade", strings.NewReader(bigJSON))
	req.Header.Set("X-MS-Internal-Secret", "secret")
	rec := httptest.NewRecorder()
	m.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413 for oversized body, got %d", rec.Code)
	}
}

func TestInternalRoutes_MaxBytesLimit(t *testing.T) {
	m := newTestModuleWithSecret(t, "test")

	// Handler must read the body for MaxBytesReader to trigger.
	m.OnEvent("big-event", func(w http.ResponseWriter, r *http.Request) {
		var v json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	// Build a body > 1 MB to exceed the internal route cap
	padding := strings.Repeat("a", 1<<20+1)
	bigJSON := `{"data":"` + padding + `"}`
	req := httptest.NewRequest("POST", "/__mirrorstack/events/big-event", strings.NewReader(bigJSON))
	req.Header.Set("X-MS-Internal-Secret", "secret")
	rec := httptest.NewRecorder()
	m.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413 for oversized internal route body, got %d", rec.Code)
	}
}

func TestScopesPanic_BeforeInit(t *testing.T) {
	fns := map[string]func(){
		"Platform":           func() { Platform(func(r chi.Router) {}) },
		"Public":             func() { Public(func(r chi.Router) {}) },
		"Internal":           func() { Internal(func(r chi.Router) {}) },
		"RegisterPermission": func() { RegisterPermission("media.view", PermissionOpts{DefaultRole: p.Admin()}) },
		"RequirePermission":  func() { RequirePermission("media.view") },
		"OnEvent":            func() { OnEvent("user.created", func(w http.ResponseWriter, r *http.Request) {}) },
		"Emits":              func() { Emits("created") },
		"Cron":               func() { Cron("cleanup", "0 3 * * *", func(w http.ResponseWriter, r *http.Request) {}) },
		"OnTask":             func() { OnTask("work", func(ctx context.Context, p json.RawMessage) error { return nil }) },
		"RunTask":            func() { _, _ = RunTask(context.Background(), "work", nil) },
		"Meter":              func() { _ = Meter(context.Background()).Record("m", 1) },
		"ModuleDB":           func() { _, _, _ = ModuleDB(context.Background()) },
		"ModuleTx":           func() { _ = ModuleTx(context.Background(), func(q db.Querier) error { return nil }) },
		"DependsOn":          func() { DependsOn("other") },
	}
	for name, fn := range fns {
		t.Run(name, func(t *testing.T) {
			resetDefault(t)
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("expected panic for %s before Init", name)
				}
			}()
			fn()
		})
	}
}

func TestModule_ModuleSchemaFor_DevFallback(t *testing.T) {
	t.Parallel()

	// Pin the dev/legacy fallback convention: mod_<id>. The platform's
	// per-module DB role provisioning AND the pre-Phase-2 backfill of
	// module_install.prefix both depend on this exact shape.
	cases := []struct {
		id   string
		want string
	}{
		{"media", "mod_media"},
		{"oauth", "mod_oauth"},
		{"billing_engine", "mod_billing_engine"},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			m, _ := New(Config{ID: tc.id})
			if got := m.moduleSchemaFor(context.Background()); got != tc.want {
				t.Errorf("moduleSchemaFor(empty ctx) = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestModule_ModuleSchemaFor_PrefixFromContext(t *testing.T) {
	t.Parallel()

	// When the platform's invoke shim has resolved the live prefix and
	// injected it via db.WithPrefix, moduleSchemaFor returns the injected
	// value verbatim. This is the production path.
	m, _ := New(Config{ID: "oauth"})
	ctx := db.WithPrefix(context.Background(), "anna_oauth_")
	if got := m.moduleSchemaFor(ctx); got != "anna_oauth_" {
		t.Errorf("moduleSchemaFor(ctx with prefix) = %q, want %q", got, "anna_oauth_")
	}
}
