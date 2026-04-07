package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestInternalAuth_NoSecret_NotLambda(t *testing.T) {
	// Dev / HTTP server: secret intentionally absent → preserve the historical
	// 401 so local tooling probing endpoints sees an auth-failure shape.
	t.Setenv("MS_INTERNAL_SECRET", "")
	handler := internalAuth(false)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest("POST", "/event", nil)
	req.Header.Set("X-MS-Internal-Secret", "anything")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 not-lambda + no secret, got %d", rec.Code)
	}
}

func TestInternalAuth_NoSecret_InLambda(t *testing.T) {
	// Lambda + no secret = operator misconfiguration. Return 503 so the
	// platform's alerting can distinguish it from wrong-secret events (401).
	// Module.Start() also fail-fasts on this state, so this branch is the
	// runtime safety net for any path that bypasses Start.
	t.Setenv("MS_INTERNAL_SECRET", "")
	handler := internalAuth(true)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest("POST", "/event", nil)
	req.Header.Set("X-MS-Internal-Secret", "anything")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 in-lambda + no secret, got %d", rec.Code)
	}
	// SECURITY: body MUST be generic. Confirming "misconfigured" or
	// "internal secret" to anonymous callers is recon. The 503 status itself
	// is the platform's signal; the detailed reason is in the server log.
	body := rec.Body.String()
	if strings.Contains(body, "misconfigured") || strings.Contains(body, "internal secret") {
		t.Errorf("503 body leaks operator state: %q", body)
	}
}

func TestInternalAuth_WrongSecret_InLambda_Still401(t *testing.T) {
	// Only the no-secret-configured state escalates to 503 in Lambda. Wrong
	// secret stays 401 so platform alerts can distinguish attacker /
	// misrouted traffic from operator misconfiguration.
	t.Setenv("MS_INTERNAL_SECRET", "test-secret-123")
	handler := internalAuth(true)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest("POST", "/event", nil)
	req.Header.Set("X-MS-Internal-Secret", "wrong-secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 wrong-secret in-lambda, got %d", rec.Code)
	}
}

func TestIsLambdaEnv(t *testing.T) {
	// Regression guard: this duplicates internal/runtime.IsLambda because of
	// an import cycle (see middleware.go). If AWS_LAMBDA_FUNCTION_NAME ever
	// stops being the right signal, both call sites need updating.
	t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "")
	if isLambdaEnv() {
		t.Error("expected isLambdaEnv() = false when AWS_LAMBDA_FUNCTION_NAME unset")
	}
	t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "test-fn")
	if !isLambdaEnv() {
		t.Error("expected isLambdaEnv() = true when AWS_LAMBDA_FUNCTION_NAME set")
	}
}
