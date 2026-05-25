package auth

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
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

func TestPlatformAuth_AnyRoleInContextAllowed(t *testing.T) {
	// Identity preset upstream (Lambda authorizer path) wins regardless of
	// env state, so this test pins down "ctx wins" without caring about
	// MS_INTERNAL_SECRET. Forces in-lambda + no secret to prove we honor
	// preset Identity even when other branches would 503.
	t.Setenv("MS_INTERNAL_SECRET", "")
	handler := platformAuth(true)(http.HandlerFunc(okHandler))

	for _, role := range []string{RoleAdmin, RoleMember, RoleViewer, "VideoManager"} {
		req := requestWithRole("GET", "/items", role)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("role %q: expected 200, got %d", role, rec.Code)
		}
	}
}

func TestPlatformAuth_LocalBypass_InjectsAdminIdentity(t *testing.T) {
	// Local dev with no secret: synthetic admin identity is injected so
	// /platform/* routes work in `mirrorstack dev` without tunnel.
	t.Setenv("MS_INTERNAL_SECRET", "")
	var seen *Identity
	handler := platformAuth(false)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = Get(r.Context())
	}))

	req := httptest.NewRequest("GET", "/platform/users", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK && rec.Code != 0 {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if seen == nil {
		t.Fatal("expected synthetic Identity to be injected; got nil")
	}
	if seen.AppRole != RoleAdmin {
		t.Errorf("synthetic identity must be admin so RequirePermission passes; got %q", seen.AppRole)
	}
}

func TestPlatformAuth_NoSecret_InLambda_503(t *testing.T) {
	// Lambda + no secret = operator misconfig. Match InternalAuth's 503.
	t.Setenv("MS_INTERNAL_SECRET", "")
	handler := platformAuth(true)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest("GET", "/platform/users", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestPlatformAuth_SecretSet_NoHeader_401(t *testing.T) {
	t.Setenv("MS_INTERNAL_SECRET", "real-secret")
	handler := platformAuth(false)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest("GET", "/platform/users", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestPlatformAuth_SecretSet_WrongSecret_401(t *testing.T) {
	t.Setenv("MS_INTERNAL_SECRET", "real-secret")
	handler := platformAuth(false)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest("GET", "/platform/users", nil)
	req.Header.Set(HeaderInternalSecret, "wrong")
	req.Header.Set(HeaderUserID, "u-1")
	req.Header.Set(HeaderAppID, "a-1")
	req.Header.Set(HeaderAppRole, RoleAdmin)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestPlatformAuth_SecretSet_MissingIdentityHeaders_401(t *testing.T) {
	// Valid secret but no identity → 401. The trusted-forwarder must
	// assert who the user is; we don't fabricate one for them.
	t.Setenv("MS_INTERNAL_SECRET", "real-secret")
	handler := platformAuth(false)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest("GET", "/platform/users", nil)
	req.Header.Set(HeaderInternalSecret, "real-secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "identity headers required") {
		t.Errorf("expected explanatory body, got %q", rec.Body.String())
	}
}

func TestPlatformAuth_SecretSet_ValidHeaders_InjectsIdentity(t *testing.T) {
	t.Setenv("MS_INTERNAL_SECRET", "real-secret")
	var seen *Identity
	handler := platformAuth(false)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = Get(r.Context())
	}))

	req := httptest.NewRequest("GET", "/platform/users", nil)
	req.Header.Set(HeaderInternalSecret, "real-secret")
	req.Header.Set(HeaderUserID, "u-123")
	req.Header.Set(HeaderAppID, "a-456")
	req.Header.Set(HeaderAppRole, RoleViewer)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK && rec.Code != 0 {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if seen == nil {
		t.Fatal("expected Identity to be injected from headers; got nil")
	}
	if seen.UserID != "u-123" || seen.AppID != "a-456" || seen.AppRole != RoleViewer {
		t.Errorf("identity mismatch: got %+v", seen)
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

func TestInternalAuth_NoSecret_NotLambda_Bypasses(t *testing.T) {
	// Local dev with no secret configured: bypass auth so `mirrorstack dev`
	// can serve /__mirrorstack/* directly without the developer exporting a
	// secret. Tunnel mode (where the platform reaches localhost via
	// dispatch) is responsible for setting the secret so this branch
	// doesn't fire.
	t.Setenv("MS_INTERNAL_SECRET", "")
	handler := internalAuth(false)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest("POST", "/event", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 bypass not-lambda + no secret, got %d", rec.Code)
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

func TestInternalAuth_LogsOnNoSecret_Lambda(t *testing.T) {
	// In Lambda mode (rejection path), each rejected request emits a log
	// line including the path. Local mode bypasses so there's no per-
	// request log there — only the construction-time "bypass auth" notice.
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	t.Setenv("MS_INTERNAL_SECRET", "")
	handler := internalAuth(true)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest("POST", "/platform/lifecycle/tick", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	got := buf.String()
	if !strings.Contains(got, "no secret configured") {
		t.Errorf("expected log to mention 'no secret configured', got: %q", got)
	}
	if !strings.Contains(got, "/platform/lifecycle/tick") {
		t.Errorf("expected log to mention request path, got: %q", got)
	}
}

func TestInternalAuth_LogsBypassNotice_LocalNoSecret(t *testing.T) {
	// Construction-time notice: local dev + no secret prints a single
	// "bypass auth (local dev)" line so the operator knows internal routes
	// are open. Per-request logs are silent in this mode.
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	t.Setenv("MS_INTERNAL_SECRET", "")
	_ = internalAuth(false) // construction-time log fires here

	got := buf.String()
	if !strings.Contains(got, "bypass auth") {
		t.Errorf("expected construction log to mention 'bypass auth', got: %q", got)
	}
}

func TestInternalAuth_LogsOnSecretMismatch(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	t.Setenv("MS_INTERNAL_SECRET", "real-secret")
	handler := internalAuth(false)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest("POST", "/platform/lifecycle/tick", nil)
	req.Header.Set("X-MS-Internal-Secret", "wrong")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	got := buf.String()
	if !strings.Contains(got, "secret mismatch") {
		t.Errorf("expected log to mention 'secret mismatch', got: %q", got)
	}
	if !strings.Contains(got, "header_present=true") {
		t.Errorf("expected header_present=true in log, got: %q", got)
	}
	// SECURITY: actual secret must never appear in log
	if strings.Contains(got, "real-secret") || strings.Contains(got, "wrong") {
		t.Errorf("log leaks secret value: %q", got)
	}
}

func TestInternalAuth_LogsHeaderAbsent(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	t.Setenv("MS_INTERNAL_SECRET", "real-secret")
	handler := internalAuth(false)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest("POST", "/platform/lifecycle/tick", nil) // no header
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	got := buf.String()
	if !strings.Contains(got, "header_present=false") {
		t.Errorf("expected header_present=false in log, got: %q", got)
	}
}
