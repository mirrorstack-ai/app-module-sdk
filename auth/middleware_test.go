package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func requestWithRole(method, path, role string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	if role != "" {
		req = req.WithContext(Set(req.Context(), Identity{AppRole: role}))
	}
	return req
}

func TestPlatformAuth_NoRole(t *testing.T) {
	handler := PlatformAuth()(http.HandlerFunc(okHandler))
	req := httptest.NewRequest("GET", "/items", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestPlatformAuth_AnyRoleAllowed(t *testing.T) {
	handler := PlatformAuth()(http.HandlerFunc(okHandler))

	for _, role := range []string{RoleAdmin, RoleMember, RoleViewer} {
		req := requestWithRole("GET", "/items", role)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("role %q: expected 200, got %d", role, rec.Code)
		}
	}
}

func TestPlatformAuth_CustomRole(t *testing.T) {
	handler := PlatformAuth()(http.HandlerFunc(okHandler))

	// Custom role passes PlatformAuth (authentication gate — any non-empty role)
	req := requestWithRole("GET", "/items", "VideoManager")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for custom role, got %d", rec.Code)
	}
}

func TestPublicAuth_Anonymous(t *testing.T) {
	handler := PublicAuth()(http.HandlerFunc(okHandler))
	req := httptest.NewRequest("GET", "/items", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestInternalAuth_NoSecret(t *testing.T) {
	t.Setenv("MS_INTERNAL_SECRET", "test-secret-123")
	handler := InternalAuth()(http.HandlerFunc(okHandler))

	req := httptest.NewRequest("POST", "/event", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestInternalAuth_WrongSecret(t *testing.T) {
	t.Setenv("MS_INTERNAL_SECRET", "test-secret-123")
	handler := InternalAuth()(http.HandlerFunc(okHandler))

	req := httptest.NewRequest("POST", "/event", nil)
	req.Header.Set("X-MS-Internal-Secret", "wrong-secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestInternalAuth_ValidSecret(t *testing.T) {
	t.Setenv("MS_INTERNAL_SECRET", "test-secret-123")
	handler := InternalAuth()(http.HandlerFunc(okHandler))

	req := httptest.NewRequest("POST", "/event", nil)
	req.Header.Set("X-MS-Internal-Secret", "test-secret-123")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestInternalAuth_NoEnvVar(t *testing.T) {
	t.Setenv("MS_INTERNAL_SECRET", "")
	handler := InternalAuth()(http.HandlerFunc(okHandler))

	req := httptest.NewRequest("POST", "/event", nil)
	req.Header.Set("X-MS-Internal-Secret", "anything")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 when env not set, got %d", rec.Code)
	}
}
