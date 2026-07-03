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

// A payload-trusted request (entered via runtime.NewLambdaHandler — real
// Lambda or the dev shim behind its envelope-secret gate) passes internal
// auth with NO header, even when a secret is configured: the deployed path
// has no per-session tunnel token to present. Mirrors RequireProxy.
func TestInternalAuth_PayloadTrusted_NoHeader_Passes(t *testing.T) {
	t.Setenv("MS_PLATFORM_TOKEN", "tunnel-session-token")
	handler := InternalAuth()(http.HandlerFunc(okHandler))

	req := httptest.NewRequest("POST", "/__mirrorstack/platform/lifecycle/app/install", nil)
	req = req.WithContext(WithPayloadTrust(req.Context()))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for payload-trusted request, got %d", rec.Code)
	}

	// The same request WITHOUT the trust mark must still be rejected —
	// trust never comes from inbound request data.
	untrusted := httptest.NewRequest("POST", "/__mirrorstack/platform/lifecycle/app/install", nil)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, untrusted)
	if rec2.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for untrusted request, got %d", rec2.Code)
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

// --- MS_PLATFORM_TOKEN tests ---

func TestInternalAuth_PlatformToken_ValidToken(t *testing.T) {
	t.Setenv("MS_PLATFORM_TOKEN", "pt-secret-456")
	t.Setenv("MS_INTERNAL_SECRET", "old-secret")
	handler := internalAuth(false)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest("POST", "/event", nil)
	req.Header.Set(HeaderPlatformToken, "pt-secret-456")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 with valid platform token, got %d", rec.Code)
	}
}

func TestInternalAuth_PlatformToken_WrongToken_401(t *testing.T) {
	t.Setenv("MS_PLATFORM_TOKEN", "pt-secret-456")
	t.Setenv("MS_INTERNAL_SECRET", "old-secret")
	handler := internalAuth(false)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest("POST", "/event", nil)
	req.Header.Set(HeaderPlatformToken, "wrong")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with wrong platform token, got %d", rec.Code)
	}
}

func TestInternalAuth_PlatformToken_OldHeaderIgnored(t *testing.T) {
	// When MS_PLATFORM_TOKEN is set, X-MS-Internal-Secret is not checked.
	t.Setenv("MS_PLATFORM_TOKEN", "pt-secret-456")
	t.Setenv("MS_INTERNAL_SECRET", "old-secret")
	handler := internalAuth(false)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest("POST", "/event", nil)
	req.Header.Set(HeaderInternalSecret, "old-secret") // correct old secret, wrong header
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 when sending old header while platform token is configured, got %d", rec.Code)
	}
}

func TestInternalAuth_PlatformToken_FallbackToInternalSecret(t *testing.T) {
	// When MS_PLATFORM_TOKEN is empty, falls back to MS_INTERNAL_SECRET.
	t.Setenv("MS_PLATFORM_TOKEN", "")
	t.Setenv("MS_INTERNAL_SECRET", "old-secret")
	handler := internalAuth(false)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest("POST", "/event", nil)
	req.Header.Set(HeaderInternalSecret, "old-secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 with fallback to internal secret, got %d", rec.Code)
	}
}

func TestInternalAuth_PlatformToken_NeitherSet_Bypass(t *testing.T) {
	t.Setenv("MS_PLATFORM_TOKEN", "")
	t.Setenv("MS_INTERNAL_SECRET", "")
	handler := internalAuth(false)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest("POST", "/event", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 bypass when neither secret set, got %d", rec.Code)
	}
}

func TestPlatformAuth_PlatformToken_ValidToken_InjectsIdentity(t *testing.T) {
	t.Setenv("MS_PLATFORM_TOKEN", "pt-secret-789")
	t.Setenv("MS_INTERNAL_SECRET", "")
	var seen *Identity
	handler := platformAuth(false)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = Get(r.Context())
	}))

	req := httptest.NewRequest("GET", "/platform/users", nil)
	req.Header.Set(HeaderPlatformToken, "pt-secret-789")
	req.Header.Set(HeaderUserID, "u-pt-1")
	req.Header.Set(HeaderAppID, "a-pt-2")
	req.Header.Set(HeaderAppRole, RoleMember)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK && rec.Code != 0 {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if seen == nil {
		t.Fatal("expected Identity from platform token path; got nil")
	}
	if seen.UserID != "u-pt-1" || seen.AppID != "a-pt-2" || seen.AppRole != RoleMember {
		t.Errorf("identity mismatch: got %+v", seen)
	}
}

func TestPlatformAuth_PlatformToken_WrongToken_401(t *testing.T) {
	t.Setenv("MS_PLATFORM_TOKEN", "pt-secret-789")
	t.Setenv("MS_INTERNAL_SECRET", "")
	handler := platformAuth(false)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest("GET", "/platform/users", nil)
	req.Header.Set(HeaderPlatformToken, "wrong")
	req.Header.Set(HeaderUserID, "u-1")
	req.Header.Set(HeaderAppID, "a-1")
	req.Header.Set(HeaderAppRole, RoleAdmin)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with wrong platform token, got %d", rec.Code)
	}
}

func TestPlatformAuth_PlatformToken_OldHeaderIgnored(t *testing.T) {
	t.Setenv("MS_PLATFORM_TOKEN", "pt-secret-789")
	t.Setenv("MS_INTERNAL_SECRET", "old-secret")
	handler := platformAuth(false)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest("GET", "/platform/users", nil)
	req.Header.Set(HeaderInternalSecret, "old-secret") // old header, should be ignored
	req.Header.Set(HeaderUserID, "u-1")
	req.Header.Set(HeaderAppID, "a-1")
	req.Header.Set(HeaderAppRole, RoleAdmin)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 when sending old header while platform token is configured, got %d", rec.Code)
	}
}

// --- RequireProxy (not_proxied guard) ---

func TestRequireProxy_NoSecret_Inert(t *testing.T) {
	// Pure standalone `go test`: no platform token configured → the guard MUST
	// pass through so module unit tests run unmodified.
	t.Setenv("MS_PLATFORM_TOKEN_FILE", "")
	t.Setenv("MS_PLATFORM_TOKEN", "")
	t.Setenv("MS_INTERNAL_SECRET", "")
	handler := requireProxy(false)(http.HandlerFunc(okHandler))

	// Even a spoofed app-id header is fine here — there's no token to enforce
	// against, so the surface is simply open (local dev / unit test).
	req := httptest.NewRequest("GET", "/public/me", nil)
	req.Header.Set(HeaderAppID, "spoofed-app")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 (inert without configured token), got %d", rec.Code)
	}
}

func TestRequireProxy_InLambda_PassThrough(t *testing.T) {
	// In Lambda the runtime strips X-MS-* headers and injects identity from the
	// typed payload, so there is no token header to match — the guard must pass
	// through (otherwise every Lambda request 403s).
	t.Setenv("MS_PLATFORM_TOKEN", "tok")
	handler := requireProxy(true)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest("GET", "/public/me", nil) // no headers (stripped)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 (Lambda payload is the trust boundary), got %d", rec.Code)
	}
}

func TestRequireProxy_PayloadTrusted_PassThrough(t *testing.T) {
	// Dev lambda-invoke shim path: identity was injected from the typed
	// envelope behind the shim's secret gate and ctx carries the payload-trust
	// mark. The guard must pass through exactly like Lambda mode — the
	// envelope never carries the per-session X-MS-Platform-Token, so matching
	// the header would 403 every shim-delivered request.
	t.Setenv("MS_PLATFORM_TOKEN", "real-token")
	handler := requireProxy(false)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest("GET", "/public/me", nil) // no token header
	req = req.WithContext(WithPayloadTrust(req.Context()))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 (payload-trusted request passes the guard), got %d", rec.Code)
	}
}

func TestRequireProxy_TokenConfigured_NoHeader_403NotProxied(t *testing.T) {
	// HTTP dev/tunnel path with a token configured: a direct caller (no token)
	// is rejected with 403 not_proxied.
	t.Setenv("MS_PLATFORM_TOKEN", "real-token")
	handler := requireProxy(false)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest("GET", "/public/me", nil)
	req.Header.Set(HeaderAppID, "spoofed-app") // attacker tries to assert app context
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), CodeNotProxied) {
		t.Errorf("expected body to carry code %q, got %s", CodeNotProxied, rec.Body.String())
	}
}

func TestRequireProxy_TokenConfigured_WrongToken_403(t *testing.T) {
	t.Setenv("MS_PLATFORM_TOKEN", "real-token")
	handler := requireProxy(false)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest("GET", "/public/me", nil)
	req.Header.Set(HeaderPlatformToken, "wrong-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for wrong token, got %d", rec.Code)
	}
}

func TestRequireProxy_TokenConfigured_ValidToken_PassThrough(t *testing.T) {
	// The proxied request (dispatch injected the matching X-MS-Platform-Token)
	// is trusted and reaches the handler.
	t.Setenv("MS_PLATFORM_TOKEN", "real-token")
	handler := requireProxy(false)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest("GET", "/public/me", nil)
	req.Header.Set(HeaderPlatformToken, "real-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for valid proxied request, got %d", rec.Code)
	}
}

func TestRequireProxy_LegacyInternalSecretHeader(t *testing.T) {
	// When only MS_INTERNAL_SECRET is configured (legacy / internal-secret-only
	// sessions), the guard matches the X-MS-Internal-Secret header — same
	// fallback platformSecretReader uses for internalAuth.
	t.Setenv("MS_PLATFORM_TOKEN_FILE", "")
	t.Setenv("MS_PLATFORM_TOKEN", "")
	t.Setenv("MS_INTERNAL_SECRET", "legacy-secret")
	handler := requireProxy(false)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest("GET", "/public/me", nil)
	req.Header.Set(HeaderInternalSecret, "legacy-secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 with valid legacy internal secret, got %d", rec.Code)
	}
}

func TestRequireProxy_TokenFile_Refresh(t *testing.T) {
	// MS_PLATFORM_TOKEN_FILE is re-read per request so a CLI reconnect that
	// rotates the token is picked up without restarting the module.
	dir := t.TempDir()
	file := dir + "/token"
	if err := os.WriteFile(file, []byte("first-token\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	t.Setenv("MS_PLATFORM_TOKEN_FILE", file)
	handler := requireProxy(false)(http.HandlerFunc(okHandler))

	req := httptest.NewRequest("GET", "/public/me", nil)
	req.Header.Set(HeaderPlatformToken, "first-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with first token, got %d", rec.Code)
	}

	// Rotate the token on disk; the old header should now be rejected.
	if err := os.WriteFile(file, []byte("second-token\n"), 0o600); err != nil {
		t.Fatalf("rotate token file: %v", err)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/public/me", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 after token rotation with no/old header, got %d", rec.Code)
	}
}

func TestRequireProxy_TokenFile_ReadError_FailsClosed(t *testing.T) {
	// MS_PLATFORM_TOKEN_FILE is set (the operator intends enforcement) but the
	// file can't be read (missing/deleted/bad perms/mid-rotation). The guard
	// must FAIL CLOSED — 403 not_proxied — not silently pass through. A
	// transient I/O error turning the guard inert would be a security hole.
	missing := t.TempDir() + "/does-not-exist"
	t.Setenv("MS_PLATFORM_TOKEN_FILE", missing)
	handler := requireProxy(false)(http.HandlerFunc(okHandler))

	// Even a request that carries SOME token header must be rejected: there is
	// no readable expected value to match against, so nothing can be trusted.
	req := httptest.NewRequest("GET", "/public/me", nil)
	req.Header.Set(HeaderPlatformToken, "anything")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when token file is unreadable, got %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, CodeNotProxied) {
		t.Errorf("expected %q error code in body, got %q", CodeNotProxied, body)
	}
}

func TestRequireProxy_TokenFile_ReadError_NotMistakenForUnconfigured(t *testing.T) {
	// Guards against a regression where a token-file read error collapses to
	// the "no secret configured" path (configured=false) and bypasses. Here the
	// file is configured but unreadable, so SecretConfigured stays true and the
	// guard enforces.
	missing := t.TempDir() + "/does-not-exist"
	t.Setenv("MS_PLATFORM_TOKEN_FILE", missing)
	if !SecretConfigured() {
		t.Fatal("SecretConfigured() must be true when MS_PLATFORM_TOKEN_FILE is set even if unreadable")
	}
}

// --- RequireProxy identity promotion (Public-route trusted app id) ---

func TestRequireProxy_ValidToken_PromotesIdentity(t *testing.T) {
	// The whole point of the #236 redesign: on a Public route, after the proxy
	// token validates, the dispatch-injected X-MS-* headers are promoted to
	// auth.Identity so the handler can read its TRUSTED app id via auth.Get —
	// PlatformAuth doesn't run on Public, so without this promotion AppID is
	// empty there.
	t.Setenv("MS_PLATFORM_TOKEN", "real-token")
	var seen *Identity
	handler := requireProxy(false)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = Get(r.Context())
	}))

	req := httptest.NewRequest("GET", "/public/me", nil)
	req.Header.Set(HeaderPlatformToken, "real-token")
	req.Header.Set(HeaderUserID, "u-pub-1")
	req.Header.Set(HeaderAppID, "a-pub-2")
	req.Header.Set(HeaderAppRole, RoleViewer)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid proxied request, got %d", rec.Code)
	}
	if seen == nil {
		t.Fatal("expected promoted Identity on the context; got nil")
	}
	if seen.AppID != "a-pub-2" {
		t.Errorf("AppID = %q, want a-pub-2 (the trusted, dispatch-injected app id)", seen.AppID)
	}
	if seen.UserID != "u-pub-1" || seen.AppRole != RoleViewer {
		t.Errorf("identity mismatch: got %+v", seen)
	}
}

func TestRequireProxy_RejectedRequest_NeverPromotes(t *testing.T) {
	// A request rejected by the guard (no/wrong token) must NEVER reach the
	// handler, so no identity is ever promoted from its (spoofable) headers.
	t.Setenv("MS_PLATFORM_TOKEN", "real-token")
	reached := false
	handler := requireProxy(false)(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		reached = true
	}))

	req := httptest.NewRequest("GET", "/public/me", nil)
	// Attacker omits the token but tries to assert an app id directly.
	req.Header.Set(HeaderAppID, "spoofed-app")
	req.Header.Set(HeaderUserID, "spoofed-user")
	req.Header.Set(HeaderAppRole, RoleAdmin)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	if reached {
		t.Error("handler must not run on a rejected request — promotion would trust spoofed headers")
	}
}

func TestRequireProxy_NoSecretInert_DoesNotPromote(t *testing.T) {
	// Standalone `go test` (no token configured): the guard is inert and passes
	// through, but it must NOT promote spoofable headers — nothing validated
	// that they came from dispatch. Documented behavior: surface is open, app id
	// unset (a module's own unit test injects identity explicitly if it needs
	// one).
	t.Setenv("MS_PLATFORM_TOKEN_FILE", "")
	t.Setenv("MS_PLATFORM_TOKEN", "")
	t.Setenv("MS_INTERNAL_SECRET", "")
	var seen *Identity
	handler := requireProxy(false)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = Get(r.Context())
	}))

	req := httptest.NewRequest("GET", "/public/me", nil)
	req.Header.Set(HeaderAppID, "spoofed-app")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (inert without configured token), got %d", rec.Code)
	}
	if seen != nil {
		t.Errorf("inert guard must not promote unvalidated headers; got identity %+v", seen)
	}
}

func TestRequireProxy_PresetIdentity_NotClobbered(t *testing.T) {
	// Lambda's InjectResources sets identity from the typed payload BEFORE any
	// header promotion could run. The guard must never overwrite a preset
	// identity with header values. Simulate by presetting on the context and
	// sending conflicting headers on the validated-token path.
	t.Setenv("MS_PLATFORM_TOKEN", "real-token")
	var seen *Identity
	handler := requireProxy(false)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = Get(r.Context())
	}))

	req := httptest.NewRequest("GET", "/public/me", nil)
	req.Header.Set(HeaderPlatformToken, "real-token")
	req.Header.Set(HeaderAppID, "header-app")
	req = req.WithContext(Set(req.Context(), Identity{
		UserID:  "preset-user",
		AppID:   "preset-app",
		AppRole: RoleAdmin,
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if seen == nil || seen.AppID != "preset-app" {
		t.Errorf("preset identity must win over headers; got %+v", seen)
	}
}
