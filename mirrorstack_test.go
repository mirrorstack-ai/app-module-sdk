package mirrorstack

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
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

func TestScopes(t *testing.T) {
	m, err := New(Config{ID: "test", Name: "Test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m.Platform(func(r chi.Router) {
		r.Get("/admin/items", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("platform"))
		})
	})
	m.Public(func(r chi.Router) {
		r.Get("/public/items", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("public"))
		})
	})
	m.Internal(func(r chi.Router) {
		r.Post("/internal/event", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("internal"))
		})
	})

	tests := []struct {
		method, path, want string
	}{
		{"GET", "/admin/items", "platform"},
		{"GET", "/public/items", "public"},
		{"POST", "/internal/event", "internal"},
	}
	for _, tt := range tests {
		rec := doRequest(t, m.Router(), tt.method, tt.path)
		if rec.Code != 200 {
			t.Errorf("%s %s: expected 200, got %d", tt.method, tt.path, rec.Code)
		}
		if rec.Body.String() != tt.want {
			t.Errorf("%s %s: expected %q, got %q", tt.method, tt.path, tt.want, rec.Body.String())
		}
	}
}

func TestHealthAutoMounted(t *testing.T) {
	m, err := New(Config{ID: "test", Name: "Test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rec := doRequest(t, m.Router(), "GET", "/__mirrorstack/health")
	if rec.Code != 200 {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected application/json, got %q", ct)
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
	if DefaultModule().Config().ID != "media" {
		t.Errorf("expected ID 'media', got %q", DefaultModule().Config().ID)
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
			t.Error("expected panic when calling Start() before Init()")
		}
	}()
	_ = Start()
}

func TestScopesPanic_BeforeInit(t *testing.T) {
	fns := map[string]func(){
		"Platform": func() { Platform(func(r chi.Router) {}) },
		"Public":   func() { Public(func(r chi.Router) {}) },
		"Internal": func() { Internal(func(r chi.Router) {}) },
	}
	for name, fn := range fns {
		t.Run(name, func(t *testing.T) {
			resetDefault(t)

			defer func() {
				if r := recover(); r == nil {
					t.Errorf("expected panic when calling %s() before Init()", name)
				}
			}()
			fn()
		})
	}
}
