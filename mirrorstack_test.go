package mirrorstack

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"
	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
	"github.com/mirrorstack-ai/app-module-sdk/system"
)

func resetDefault(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { defaultModule = nil })
	defaultModule = nil
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

func TestPlatform_RejectsNoAuth(t *testing.T) {
	m, _ := New(Config{ID: "test", Name: "Test"})
	m.Platform(func(r chi.Router) {
		r.Get("/admin", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("admin"))
		})
	})

	rec := doRequest(t, m.Router(), "GET", "/admin")
	if rec.Code != 401 {
		t.Errorf("expected 401 without auth, got %d", rec.Code)
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
		rec := doRequestWithRole(t, m.Router(), "GET", "/dashboard", role)
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

	rec := doRequest(t, m.Router(), "GET", "/items")
	if rec.Code != 200 {
		t.Errorf("expected 200 for anonymous, got %d", rec.Code)
	}
}

func TestInternal_RejectsNoSecret(t *testing.T) {
	t.Setenv("MS_INTERNAL_SECRET", "test-secret")
	m, _ := New(Config{ID: "test", Name: "Test"})
	m.Internal(func(r chi.Router) {
		r.Post("/event", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("internal"))
		})
	})

	rec := doRequest(t, m.Router(), "POST", "/event")
	if rec.Code != 401 {
		t.Errorf("expected 401 without secret, got %d", rec.Code)
	}
}

func TestInternal_AcceptsValidSecret(t *testing.T) {
	t.Setenv("MS_INTERNAL_SECRET", "test-secret")
	m, _ := New(Config{ID: "test", Name: "Test"})
	m.Internal(func(r chi.Router) {
		r.Post("/event", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("internal"))
		})
	})

	rec := doRequestWithSecret(t, m.Router(), "POST", "/event", "test-secret")
	if rec.Code != 200 {
		t.Errorf("expected 200 with valid secret, got %d", rec.Code)
	}
}

// --- Permission middleware ---

func TestRequirePermission_AllowsMember(t *testing.T) {
	t.Parallel() // safe now that permission state lives on the Module instance, not auth package globals

	m, _ := New(Config{ID: "test", Name: "Test"})
	m.Platform(func(r chi.Router) {
		r.With(m.RequirePermission("media.view", "admin", "member", "viewer")).Get("/items", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("ok"))
		})
	})

	rec := doRequestWithRole(t, m.Router(), "GET", "/items", auth.RoleMember)
	if rec.Code != 200 {
		t.Errorf("expected 200 for member with media.view, got %d", rec.Code)
	}
}

func TestRequirePermission_RejectsViewer(t *testing.T) {
	t.Parallel()

	m, _ := New(Config{ID: "test", Name: "Test"})
	m.Platform(func(r chi.Router) {
		r.With(m.RequirePermission("media.delete", "admin")).Delete("/items/{id}", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("deleted"))
		})
	})

	rec := doRequestWithRole(t, m.Router(), "DELETE", "/items/123", auth.RoleViewer)
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

	m1.Platform(func(r chi.Router) {
		r.With(m1.RequirePermission("media.upload", "admin", "member")).Post("/upload", func(w http.ResponseWriter, r *http.Request) {})
		r.With(m1.RequirePermission("media.view", "admin", "member", "viewer")).Get("/items", func(w http.ResponseWriter, r *http.Request) {})
	})
	m2.Platform(func(r chi.Router) {
		r.With(m2.RequirePermission("video.transcode", "admin")).Post("/transcode", func(w http.ResponseWriter, r *http.Request) {})
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

// --- System routes ---

func TestHealthAutoMounted(t *testing.T) {
	m, _ := New(Config{ID: "test", Name: "Test"})
	rec := doRequest(t, m.Router(), "GET", "/__mirrorstack/health")
	if rec.Code != 200 {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestSystemPlatformRoutes_RequireInternalSecret(t *testing.T) {
	t.Setenv("MS_INTERNAL_SECRET", "platform-secret")
	m, _ := New(Config{ID: "test", Name: "Test"})

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
	t.Setenv("MS_INTERNAL_SECRET", "platform-secret")
	m, _ := New(Config{ID: "media", Name: "Media", Icon: "perm_media"})

	rec := doRequestWithSecret(t, m.Router(), "GET", "/__mirrorstack/platform/manifest", "platform-secret")
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
	t.Setenv("MS_INTERNAL_SECRET", "secret")
	m, _ := New(Config{ID: "media", Name: "Media"})

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
		"sql/0000_initial.up.sql":   &fstest.MapFile{Data: []byte("")},
		"sql/0008_add_index.up.sql": &fstest.MapFile{Data: []byte("")},
	}
	m, _ := New(Config{ID: "media", Name: "Media", SQL: sqlFS})

	rec := doRequestWithSecret(t, m.Router(), "GET", "/__mirrorstack/platform/manifest", "secret")
	var got system.ManifestPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	if got.Migration != "0008" {
		t.Errorf("migration = %q, want 0008", got.Migration)
	}
}

// --- Lifecycle endpoints ---

func TestLifecycle_RoutesRequireInternalSecret(t *testing.T) {
	t.Setenv("MS_INTERNAL_SECRET", "secret")
	m, _ := New(Config{ID: "test"})

	// All four lifecycle routes should reject requests without the secret.
	routes := []string{
		"/__mirrorstack/platform/lifecycle/install",
		"/__mirrorstack/platform/lifecycle/upgrade",
		"/__mirrorstack/platform/lifecycle/downgrade",
		"/__mirrorstack/platform/lifecycle/uninstall",
	}
	for _, route := range routes {
		t.Run(route, func(t *testing.T) {
			rec := doRequest(t, m.Router(), "POST", route)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
		})
	}
}

func TestLifecycle_UninstallReturnsOK(t *testing.T) {
	t.Setenv("MS_INTERNAL_SECRET", "secret")
	m, _ := New(Config{ID: "test"})

	rec := doRequestWithSecret(t, m.Router(), "POST", "/__mirrorstack/platform/lifecycle/uninstall", "secret")
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Errorf("body = %s, want status:ok", rec.Body.String())
	}
}

func TestLifecycle_InstallEmptyFSReturnsOK(t *testing.T) {
	t.Setenv("MS_INTERNAL_SECRET", "secret")
	// No SQL configured → install is a no-op (no migrations to apply).
	m, _ := New(Config{ID: "test"})

	rec := doRequestWithSecret(t, m.Router(), "POST", "/__mirrorstack/platform/lifecycle/install", "secret")
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
}

func TestLifecycle_UpgradeRequiresPayload(t *testing.T) {
	t.Setenv("MS_INTERNAL_SECRET", "secret")
	m, _ := New(Config{ID: "test"})

	// No body → 400
	req := httptest.NewRequest("POST", "/__mirrorstack/platform/lifecycle/upgrade", nil)
	req.Header.Set("X-MS-Internal-Secret", "secret")
	rec := httptest.NewRecorder()
	m.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestManifest_RegisteredScopesStillRouteCorrectly(t *testing.T) {
	// Verify that the chi.Walk + re-register approach in scopedRoutes preserves
	// the original routing behavior. Routes registered via Platform/Public/Internal
	// must still be reachable AND still enforce auth.
	t.Setenv("MS_INTERNAL_SECRET", "secret")
	m, _ := New(Config{ID: "test"})

	m.Platform(func(r chi.Router) {
		r.Get("/admin/items", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("admin"))
		})
	})
	m.Public(func(r chi.Router) {
		r.Get("/public/feed", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("public"))
		})
	})

	// Public route — no auth needed
	if rec := doRequest(t, m.Router(), "GET", "/public/feed"); rec.Code != 200 {
		t.Errorf("public route: code = %d, want 200", rec.Code)
	}
	// Platform route without auth → 401
	if rec := doRequest(t, m.Router(), "GET", "/admin/items"); rec.Code != 401 {
		t.Errorf("platform route without auth: code = %d, want 401", rec.Code)
	}
	// Platform route with auth → 200
	if rec := doRequestWithRole(t, m.Router(), "GET", "/admin/items", auth.RoleAdmin); rec.Code != 200 {
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
	req := httptest.NewRequest("POST", "/__mirrorstack/platform/lifecycle/upgrade", strings.NewReader(bigJSON))
	req.Header.Set("X-MS-Internal-Secret", "secret")
	rec := httptest.NewRecorder()
	m.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413 for oversized body, got %d", rec.Code)
	}
}

func TestScopesPanic_BeforeInit(t *testing.T) {
	fns := map[string]func(){
		"Platform":          func() { Platform(func(r chi.Router) {}) },
		"Public":            func() { Public(func(r chi.Router) {}) },
		"Internal":          func() { Internal(func(r chi.Router) {}) },
		"RequirePermission": func() { RequirePermission("media.view", "admin") },
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
