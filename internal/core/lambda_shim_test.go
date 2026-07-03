package core

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/db"
)

func postShim(t *testing.T, m *Module, envelope string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", lambdaInvokePath, strings.NewReader(envelope))
	rec := httptest.NewRecorder()
	m.Router().ServeHTTP(rec, req)
	return rec
}

// shimResponse is the LambdaResponse wire shape dispatch decodes out of a 2xx
// shim body. Headers is deliberately map[string][]string — decoding pins the
// contract (a map[string]string producer would fail the unmarshal).
type shimResponse struct {
	StatusCode int                 `json:"statusCode"`
	Headers    map[string][]string `json:"headers"`
	Body       string              `json:"body"`
}

func decodeShimResponse(t *testing.T, rec *httptest.ResponseRecorder) shimResponse {
	t.Helper()
	// Exact top-level field names are the transport contract with dispatch.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode shim response: %v (body=%s)", err, rec.Body.String())
	}
	for _, key := range []string{"statusCode", "headers", "body"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("shim response missing %q field: %s", key, rec.Body.String())
		}
	}
	var out shimResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode shim response envelope: %v (body=%s)", err, rec.Body.String())
	}
	return out
}

// newShimTestModule builds a module with the lambda shim secret configured and
// three public routes the round-trip cases exercise. MS_LAMBDA_INTERNAL_SECRET
// MUST be set before New() — lambdaInvokeShim captures it at mount time.
func newShimTestModule(t *testing.T) *Module {
	t.Helper()
	t.Setenv("MS_LAMBDA_INTERNAL_SECRET", "lambda-secret")
	m, err := New(Config{ID: "test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m.Public(func(r chi.Router) {
		r.Put("/echo", func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			resp := map[string]string{
				"method":      r.Method,
				"body":        string(body),
				"contentType": r.Header.Get("Content-Type"),
				"schema":      db.SchemaFrom(r.Context()),
			}
			if id := auth.Get(r.Context()); id != nil {
				resp["userId"] = id.UserID
				resp["appId"] = id.AppID
				resp["appRole"] = id.AppRole
			}
			w.Header().Set("X-Echo", "yes")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)
		})
		r.Get("/anon", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("anon"))
		})
		r.Get("/boom", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		})
	})
	return m
}

// TestLambdaInvokeShim_RoundTrip pins the full wire contract with dispatch:
// request field names method/path/headers/body/userId/appId/appRole/appSchema,
// response field names statusCode/headers/body (headers: map[string][]string),
// and outer-status semantics (a delivered invoke is ALWAYS outer 200 — the
// module's real status, including failures, rides inside the envelope).
func TestLambdaInvokeShim_RoundTrip(t *testing.T) {
	cases := []struct {
		name       string
		envelope   string // dispatch-shaped JSON; field names are the contract
		wantInner  int
		checkInner func(t *testing.T, out shimResponse)
	}{
		{
			name: "typed identity, appSchema, headers, and body all flow",
			envelope: `{
				"method": "PUT",
				"path": "/public/echo",
				"headers": {"Content-Type": "application/json", "X-MS-Internal-Secret": "lambda-secret"},
				"body": "{\"title\":\"x\"}",
				"userId": "u-1",
				"appId": "a-1",
				"appRole": "admin",
				"appSchema": "app_a1"
			}`,
			wantInner: http.StatusCreated,
			checkInner: func(t *testing.T, out shimResponse) {
				if got := out.Headers["X-Echo"]; len(got) != 1 || got[0] != "yes" {
					t.Errorf("response headers[X-Echo] = %v, want [yes]", got)
				}
				var inner map[string]string
				if err := json.Unmarshal([]byte(out.Body), &inner); err != nil {
					t.Fatalf("decode inner body: %v", err)
				}
				want := map[string]string{
					"method":      "PUT",
					"body":        `{"title":"x"}`,
					"contentType": "application/json",
					"schema":      "app_a1",
					"userId":      "u-1",
					"appId":       "a-1",
					"appRole":     "admin",
				}
				for k, v := range want {
					if inner[k] != v {
						t.Errorf("inner %s = %q, want %q", k, inner[k], v)
					}
				}
			},
		},
		{
			name: "anonymous public route",
			envelope: `{
				"method": "GET",
				"path": "/public/anon",
				"headers": {"X-MS-Internal-Secret": "lambda-secret"}
			}`,
			wantInner: http.StatusOK,
		},
		{
			name: "handler failure rides INSIDE the envelope",
			envelope: `{
				"method": "GET",
				"path": "/public/boom",
				"headers": {"X-MS-Internal-Secret": "lambda-secret"}
			}`,
			wantInner: http.StatusInternalServerError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newShimTestModule(t)
			rec := postShim(t, m, tc.envelope)
			if rec.Code != http.StatusOK {
				t.Fatalf("outer status = %d, want 200 (dispatch treats >=300 as transport fault); body=%s", rec.Code, rec.Body.String())
			}
			out := decodeShimResponse(t, rec)
			if out.StatusCode != tc.wantInner {
				t.Errorf("envelope statusCode = %d, want %d (body=%s)", out.StatusCode, tc.wantInner, out.Body)
			}
			if tc.checkInner != nil {
				tc.checkInner(t, out)
			}
		})
	}
}

// TestLambdaInvokeShim_Gate pins the secret gate: with MS_LAMBDA_INTERNAL_SECRET
// configured, a forged envelope (absent/wrong secret) is rejected with an outer
// 401 and the routed handler never runs.
func TestLambdaInvokeShim_Gate(t *testing.T) {
	cases := []struct {
		name       string
		headers    string // headers object inside the envelope
		wantStatus int
	}{
		{"absent secret", `{}`, http.StatusUnauthorized},
		{"wrong secret", `{"X-MS-Internal-Secret": "nope"}`, http.StatusUnauthorized},
		{"correct secret", `{"X-MS-Internal-Secret": "lambda-secret"}`, http.StatusOK},
		{"correct secret, differently-cased key", `{"x-ms-internal-secret": "lambda-secret"}`, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MS_LAMBDA_INTERNAL_SECRET", "lambda-secret")
			m, err := New(Config{ID: "test"})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			served := false
			m.Public(func(r chi.Router) {
				r.Get("/probe", func(w http.ResponseWriter, r *http.Request) { served = true })
			})

			rec := postShim(t, m, `{"method":"GET","path":"/public/probe","headers":`+tc.headers+`}`)
			if rec.Code != tc.wantStatus {
				t.Fatalf("outer status = %d, want %d (body=%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if wantServed := tc.wantStatus == http.StatusOK; served != wantServed {
				t.Errorf("route served = %v, want %v", served, wantServed)
			}
		})
	}
}

func TestLambdaInvokeShim_EnforcingWithoutLambdaSecret_FailsClosed(t *testing.T) {
	// A platform-secret source configured (tunnel / self-hosted enforcing mode)
	// but no MS_LAMBDA_INTERNAL_SECRET: the shim must 503 and never invoke —
	// the operator intended enforcement, so the missing lambda secret must not
	// degrade into a bypass (tunnel mode never bypasses auth). Sending the
	// platform secret's VALUE in the envelope must not help: the shim gates on
	// its own dedicated secret, not the tunnel token.
	t.Setenv("MS_INTERNAL_SECRET", "tunnel-secret")
	t.Setenv("MS_LAMBDA_INTERNAL_SECRET", "")
	m, err := New(Config{ID: "test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	served := false
	m.Public(func(r chi.Router) {
		r.Get("/probe", func(w http.ResponseWriter, r *http.Request) { served = true })
	})

	rec := postShim(t, m, `{"method":"GET","path":"/public/probe","headers":{"X-MS-Internal-Secret":"tunnel-secret"}}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("outer status = %d, want 503 (fail closed)", rec.Code)
	}
	if served {
		t.Error("route must never be served when the shim fails closed")
	}
}

func TestLambdaInvokeShim_NothingConfigured_Bypasses(t *testing.T) {
	// Plain `mirrorstack dev` / standalone go test: no secret source of any
	// kind. The shim bypasses its gate, matching every other guard on this
	// surface (proxy guard inert, PlatformAuth synthetic admin) — the module
	// is already fully open in this state, so the shim adds no new exposure.
	t.Setenv("MS_PLATFORM_TOKEN_FILE", "")
	t.Setenv("MS_PLATFORM_TOKEN", "")
	t.Setenv("MS_INTERNAL_SECRET", "")
	t.Setenv("MS_LAMBDA_INTERNAL_SECRET", "")
	m, err := New(Config{ID: "test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m.Public(func(r chi.Router) {
		r.Get("/probe", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	})

	rec := postShim(t, m, `{"method":"GET","path":"/public/probe","headers":{}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("outer status = %d, want 200 (gate inert with nothing configured)", rec.Code)
	}
	if out := decodeShimResponse(t, rec); out.StatusCode != http.StatusOK {
		t.Errorf("envelope statusCode = %d, want 200", out.StatusCode)
	}
}

func TestLambdaInvokeShim_ProxyGuardedRoutes_Serve(t *testing.T) {
	// The dev-runner config: MS_PLATFORM_TOKEN_FILE points at the per-session
	// tunnel token, so RequireProxy ENFORCES a token that lambda envelopes
	// never carry. The payload-trust mark set behind the shim's gate must move
	// the request past the guard — without it every Public/Platform route
	// would 403 not_proxied through the shim.
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("session-token\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	t.Setenv("MS_PLATFORM_TOKEN_FILE", tokenFile)
	t.Setenv("MS_LAMBDA_INTERNAL_SECRET", "lambda-secret")
	m, err := New(Config{ID: "test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m.Public(func(r chi.Router) {
		r.Get("/start", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("started")) })
	})
	var gotIdentity auth.Identity
	m.Platform(func(r chi.Router) {
		r.Get("/whoami", func(w http.ResponseWriter, r *http.Request) {
			if id := auth.Get(r.Context()); id != nil {
				gotIdentity = *id
			}
		})
	})

	t.Run("public behind RequireProxy", func(t *testing.T) {
		rec := postShim(t, m, `{"method":"GET","path":"/public/start","headers":{"X-MS-Internal-Secret":"lambda-secret"}}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("outer status = %d, want 200", rec.Code)
		}
		out := decodeShimResponse(t, rec)
		if out.StatusCode != http.StatusOK {
			t.Errorf("envelope statusCode = %d, want 200 (not_proxied would be 403): %s", out.StatusCode, out.Body)
		}
	})

	t.Run("platform reads envelope identity via auth.Get", func(t *testing.T) {
		rec := postShim(t, m, `{"method":"GET","path":"/platform/whoami","headers":{"X-MS-Internal-Secret":"lambda-secret"},"userId":"u-7","appId":"a-7","appRole":"member"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("outer status = %d, want 200", rec.Code)
		}
		out := decodeShimResponse(t, rec)
		if out.StatusCode != http.StatusOK {
			t.Fatalf("envelope statusCode = %d, want 200: %s", out.StatusCode, out.Body)
		}
		want := auth.Identity{UserID: "u-7", AppID: "a-7", AppRole: "member"}
		if gotIdentity != want {
			t.Errorf("platform handler identity = %+v, want %+v", gotIdentity, want)
		}
	})
}

func TestLambdaInvokeShim_StripsIdentityClaimHeaders(t *testing.T) {
	// Identity-claim headers inside the envelope must still be stripped on the
	// shim path — trusted identity arrives ONLY via the typed envelope fields.
	m := newShimTestModule(t)
	var claims map[string]string
	m.Public(func(r chi.Router) {
		r.Get("/claims", func(w http.ResponseWriter, r *http.Request) {
			claims = map[string]string{
				"userID":  r.Header.Get(auth.HeaderUserID),
				"appID":   r.Header.Get(auth.HeaderAppID),
				"appRole": r.Header.Get(auth.HeaderAppRole),
			}
		})
	})

	rec := postShim(t, m, `{
		"method": "GET",
		"path": "/public/claims",
		"headers": {
			"X-MS-Internal-Secret": "lambda-secret",
			"X-MS-User-ID": "spoofed-user",
			"X-MS-App-ID": "spoofed-app",
			"X-MS-App-Role": "admin"
		}
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("outer status = %d, want 200", rec.Code)
	}
	for name, got := range claims {
		if got != "" {
			t.Errorf("identity claim header %s must be stripped through the shim, got %q", name, got)
		}
	}
}

func TestLambdaInvokeShim_RejectsSelfTargetingEnvelope(t *testing.T) {
	// An envelope addressed back at the shim would re-enter it with an
	// attacker-controlled inner body; reject before invoking.
	m := newShimTestModule(t)
	for _, path := range []string{
		"/__mirrorstack/lambda-invoke",
		"__mirrorstack/lambda-invoke",
		"/__mirrorstack/lambda-invoke?x=1",
	} {
		t.Run(path, func(t *testing.T) {
			rec := postShim(t, m, `{"method":"POST","path":"`+path+`","headers":{"X-MS-Internal-Secret":"lambda-secret"}}`)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("outer status = %d, want 400 for self-targeting envelope", rec.Code)
			}
		})
	}
}

func TestLambdaInvokeShim_MalformedEnvelope_400(t *testing.T) {
	m := newShimTestModule(t)
	rec := postShim(t, m, `not json`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("outer status = %d, want 400 for malformed envelope", rec.Code)
	}
}

func TestLambdaInvokeShim_MountedPostOnly(t *testing.T) {
	// Mount condition: outside Lambda mode (this test process) the route
	// exists, POST only. The Lambda-mode branch (route absent entirely) can't
	// be driven from a test — runtime.IsLambda() is fixed at process init —
	// so the guard in mountSystemRoutes is the reviewed line for that half.
	m := newShimTestModule(t)
	rec := doRequest(t, m.Router(), "GET", lambdaInvokePath)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET %s = %d, want 405 (POST only)", lambdaInvokePath, rec.Code)
	}
}
