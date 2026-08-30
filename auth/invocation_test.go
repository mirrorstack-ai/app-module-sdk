package auth

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/actor"
	"github.com/mirrorstack-ai/app-module-sdk/internal/invocationstate"
	"github.com/mirrorstack-ai/app-module-sdk/internal/invocationwire"
	"github.com/mirrorstack-ai/app-module-sdk/invocation"
)

const (
	testInvocationModuleID      = "11111111-1111-4111-8111-111111111111"
	testInvocationCompactID     = "m11111111111141118111111111111111"
	testInvocationModuleSlug    = "user-core"
	testInvocationPlatformToken = "typed-platform-token"
)

func invocationWire(t *testing.T, method, path string, body []byte) (invocation.Context, string) {
	t.Helper()
	raw, err := os.ReadFile("../invocation/testdata/invocation_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := invocationwire.Parse(bytes.TrimSpace(raw))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	trusted.Request.Method = method
	trusted.Request.Path = path
	trusted.Request.BodySHA256 = hex.EncodeToString(digest[:])
	trusted.Routes.CurrentLocal = path
	raw, err = invocationwire.Marshal(trusted)
	if err != nil {
		t.Fatal(err)
	}
	header, err := invocationwire.EncodeHeader(raw)
	if err != nil {
		t.Fatal(err)
	}
	return trusted, header
}

func addInvocationWire(req *http.Request, trusted invocation.Context, header string) {
	req.Header[invocationwire.Header] = []string{header}
	for name, value := range invocationwire.LegacyHeaders(trusted) {
		req.Header[name] = []string{value}
	}
}

func setTypedProxyEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("MS_PLATFORM_TOKEN_FILE", "")
	t.Setenv("MS_PLATFORM_TOKEN", testInvocationPlatformToken)
	t.Setenv("MS_INTERNAL_SECRET", "")
}

func TestRequireProxyTypedInvocationInstallsAuthoritativeContextAndStripsWire(t *testing.T) {
	setTypedProxyEnvironment(t)
	body := []byte(`{"title":"typed"}`)
	const path = "/public/items?sort=new"
	trusted, header := invocationWire(t, http.MethodPost, path, body)

	var reached bool
	handler := requireProxyForModule(false, testInvocationCompactID, testInvocationModuleSlug)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		got, ok := invocation.FromContext(r.Context())
		if !ok || got.Request.ID != trusted.Request.ID {
			t.Fatalf("trusted invocation missing or changed: %+v, %v", got, ok)
		}
		identity := Get(r.Context())
		if identity == nil || identity.UserID != trusted.Identity.UserID || identity.AppID != trusted.App.ID || identity.AppRole != trusted.Identity.AppRole {
			t.Fatalf("typed identity was not authoritative: %+v", identity)
		}
		if schema := db.SchemaFrom(r.Context()); schema != trusted.App.Schema {
			t.Fatalf("schema = %q, want %q", schema, trusted.App.Schema)
		}
		proof := invocationwire.ProofFromContext(r.Context())
		decodedProof, err := invocationwire.EncodeHeader(proof)
		if err != nil || decodedProof != header {
			t.Fatalf("private invocation proof changed: header_equal=%v err=%v", decodedProof == header, err)
		}
		proof[0] ^= 0xff
		if again := invocationwire.ProofFromContext(r.Context()); len(again) == 0 || again[0] == proof[0] {
			t.Fatal("private invocation proof shared mutable storage")
		}
		if actor.Delegation(r.Context()) != "" {
			t.Fatal("public surface activated typed actor delegation")
		}
		for name := range r.Header {
			if strings.EqualFold(name, invocationwire.Header) || isLegacyInvocationHeader(name) {
				t.Fatalf("invocation wire header reached module: %s", name)
			}
		}
		gotBody, err := io.ReadAll(r.Body)
		if err != nil || !bytes.Equal(gotBody, body) {
			t.Fatalf("restored body = %q, %v", gotBody, err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set(HeaderPlatformToken, testInvocationPlatformToken)
	addInvocationWire(req, trusted, header)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || !reached {
		t.Fatalf("response=%d body=%s reached=%v", rec.Code, rec.Body.String(), reached)
	}
}

func TestTypedActorDelegationActivatesOnlyOnPlatformSurface(t *testing.T) {
	setTypedProxyEnvironment(t)
	const path = "/platform/users"
	trusted, header := invocationWire(t, http.MethodGet, path, nil)

	var gotActor string
	handler := withPlatformSurfaceForTest(
		requireProxyForModule(false, testInvocationCompactID, testInvocationModuleSlug)(
			platformAuth(false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotActor = actor.Delegation(r.Context())
				if r.Header.Get(actor.HeaderDelegation) != "" {
					t.Fatal("raw actor header reached platform handler")
				}
				w.WriteHeader(http.StatusNoContent)
			})),
		),
	)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set(HeaderPlatformToken, testInvocationPlatformToken)
	addInvocationWire(req, trusted, header)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || gotActor != trusted.Identity.ActorDelegation {
		t.Fatalf("status=%d actor=%q want=%q", rec.Code, gotActor, trusted.Identity.ActorDelegation)
	}

	t.Setenv("MS_PLATFORM_TOKEN", "")
	t.Setenv("MS_INTERNAL_SECRET", "typed-internal-secret")
	gotActor = "not-called"
	internal := internalAuthForModule(false, testInvocationCompactID, testInvocationModuleSlug)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotActor = actor.Delegation(r.Context())
		if _, ok := invocation.FromContext(r.Context()); !ok {
			t.Fatal("internal handler lost typed invocation")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	req = httptest.NewRequest(http.MethodGet, "/internal/check", nil)
	trusted, header = invocationWire(t, http.MethodGet, "/internal/check", nil)
	req.Header.Set(HeaderInternalSecret, "typed-internal-secret")
	addInvocationWire(req, trusted, header)
	rec = httptest.NewRecorder()
	internal.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || gotActor != "" {
		t.Fatalf("internal status=%d actor=%q, want discarded", rec.Code, gotActor)
	}
}

func TestPlatformAuthStandaloneConsumesTypedInvocationAfterSecretValidation(t *testing.T) {
	t.Setenv("MS_PLATFORM_TOKEN_FILE", "")
	t.Setenv("MS_PLATFORM_TOKEN", "")
	t.Setenv("MS_INTERNAL_SECRET", "typed-internal-secret")
	const path = "/platform/standalone"
	trusted, header := invocationWire(t, http.MethodGet, path, nil)
	var gotActor string
	handler := withPlatformSurfaceForTest(platformAuth(false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := invocation.FromContext(r.Context()); !ok {
			t.Fatal("standalone PlatformAuth did not install the typed invocation")
		}
		gotActor = actor.Delegation(r.Context())
		for name := range r.Header {
			if strings.EqualFold(name, invocationwire.Header) || isLegacyInvocationHeader(name) {
				t.Fatalf("standalone PlatformAuth leaked wire header %s", name)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set(HeaderInternalSecret, "typed-internal-secret")
	addInvocationWire(req, trusted, header)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || gotActor != trusted.Identity.ActorDelegation {
		t.Fatalf("status=%d actor=%q want=%q", rec.Code, gotActor, trusted.Identity.ActorDelegation)
	}
}

func TestInternalAuthReadsCoreInstalledInvocationBinding(t *testing.T) {
	t.Setenv("MS_PLATFORM_TOKEN_FILE", "")
	t.Setenv("MS_PLATFORM_TOKEN", "")
	t.Setenv("MS_INTERNAL_SECRET", "typed-internal-secret")
	trusted, header := invocationWire(t, http.MethodGet, "/internal/check", nil)

	tests := []struct {
		name       string
		binding    invocationstate.Binding
		wantStatus int
	}{
		{
			name: "matching binding",
			binding: invocationstate.Binding{
				ModuleID: testInvocationCompactID, ModuleSlug: testInvocationModuleSlug,
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name: "different module rejected",
			binding: invocationstate.Binding{
				ModuleID: "m99999999999949998999999999999999", ModuleSlug: testInvocationModuleSlug,
			},
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := InternalAuth()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			req := httptest.NewRequest(http.MethodGet, "/internal/check", nil)
			req = req.WithContext(invocationstate.WithBinding(req.Context(), test.binding))
			req.Header.Set(HeaderInternalSecret, "typed-internal-secret")
			addInvocationWire(req, trusted, header)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status=%d body=%q want=%d", recorder.Code, recorder.Body.String(), test.wantStatus)
			}
		})
	}
}

func TestTrustedHTTPInvocationConflictsFailClosed(t *testing.T) {
	setTypedProxyEnvironment(t)
	body := []byte("bound-body")
	const path = "/public/check?q=1"
	trusted, header := invocationWire(t, http.MethodPost, path, body)

	tests := []struct {
		name       string
		method     string
		path       string
		body       []byte
		moduleID   string
		moduleSlug string
		mutate     func(*http.Request)
	}{
		{name: "method", method: http.MethodPut, path: path, body: body, moduleID: testInvocationCompactID, moduleSlug: testInvocationModuleSlug},
		{name: "path", method: http.MethodPost, path: "/public/other", body: body, moduleID: testInvocationCompactID, moduleSlug: testInvocationModuleSlug},
		{name: "body", method: http.MethodPost, path: path, body: []byte("different"), moduleID: testInvocationCompactID, moduleSlug: testInvocationModuleSlug},
		{name: "module id", method: http.MethodPost, path: path, body: body, moduleID: "m99999999999949998999999999999999", moduleSlug: testInvocationModuleSlug},
		{name: "noncanonical expected module id", method: http.MethodPost, path: path, body: body, moduleID: "legacy-module-id", moduleSlug: testInvocationModuleSlug},
		{name: "module slug", method: http.MethodPost, path: path, body: body, moduleID: testInvocationCompactID, moduleSlug: "other-module"},
		{name: "malformed", method: http.MethodPost, path: path, body: body, moduleID: testInvocationCompactID, moduleSlug: testInvocationModuleSlug, mutate: func(req *http.Request) {
			req.Header[invocationwire.Header] = []string{"not+raw-url-base64"}
		}},
		{name: "empty typed value list", method: http.MethodPost, path: path, body: body, moduleID: testInvocationCompactID, moduleSlug: testInvocationModuleSlug, mutate: func(req *http.Request) {
			req.Header[invocationwire.Header] = []string{}
		}},
		{name: "duplicate typed case variant", method: http.MethodPost, path: path, body: body, moduleID: testInvocationCompactID, moduleSlug: testInvocationModuleSlug, mutate: func(req *http.Request) {
			req.Header[strings.ToLower(invocationwire.Header)] = []string{header}
		}},
		{name: "legacy mismatch", method: http.MethodPost, path: path, body: body, moduleID: testInvocationCompactID, moduleSlug: testInvocationModuleSlug, mutate: func(req *http.Request) {
			req.Header[invocationwire.LegacyAppIDHeader] = []string{"forged-app-value"}
		}},
		{name: "duplicate legacy value", method: http.MethodPost, path: path, body: body, moduleID: testInvocationCompactID, moduleSlug: testInvocationModuleSlug, mutate: func(req *http.Request) {
			req.Header[invocationwire.LegacyRequestIDHeader] = []string{trusted.Request.ID, trusted.Request.ID}
		}},
		{name: "duplicate legacy case variant", method: http.MethodPost, path: path, body: body, moduleID: testInvocationCompactID, moduleSlug: testInvocationModuleSlug, mutate: func(req *http.Request) {
			req.Header[strings.ToLower(invocationwire.LegacyAppIDHeader)] = []string{trusted.App.ID}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reached := false
			handler := requireProxyForModule(false, test.moduleID, test.moduleSlug)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				reached = true
			}))
			req := httptest.NewRequest(test.method, test.path, bytes.NewReader(test.body))
			req.Header.Set(HeaderPlatformToken, testInvocationPlatformToken)
			addInvocationWire(req, trusted, header)
			if test.mutate != nil {
				test.mutate(req)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest || rec.Body.String() != `{"error":"invalid invocation context"}`+"\n" || reached {
				t.Fatalf("status=%d body=%q reached=%v", rec.Code, rec.Body.String(), reached)
			}
			if strings.Contains(rec.Body.String(), "forged") || strings.Contains(rec.Body.String(), trusted.App.ID) {
				t.Fatalf("error leaked invocation values: %q", rec.Body.String())
			}
		})
	}
}

func TestUnauthenticatedInvocationNeverReachesModuleWireOrContext(t *testing.T) {
	trusted, header := invocationWire(t, http.MethodGet, "/public/check", nil)

	t.Run("wrong token rejected", func(t *testing.T) {
		setTypedProxyEnvironment(t)
		reached := false
		handler := requireProxy(false)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
		req := httptest.NewRequest(http.MethodGet, "/public/check", nil)
		req.Header.Set(HeaderPlatformToken, "wrong")
		addInvocationWire(req, trusted, header)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || reached {
			t.Fatalf("status=%d reached=%v", rec.Code, reached)
		}
	})

	t.Run("local bypass strips forged typed header", func(t *testing.T) {
		t.Setenv("MS_PLATFORM_TOKEN_FILE", "")
		t.Setenv("MS_PLATFORM_TOKEN", "")
		t.Setenv("MS_INTERNAL_SECRET", "")
		var gotHeader string
		var gotContext bool
		handler := requireProxy(false)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			gotHeader = r.Header.Get(invocationwire.Header)
			_, gotContext = invocation.FromContext(r.Context())
		}))
		req := httptest.NewRequest(http.MethodGet, "/public/check", nil)
		addInvocationWire(req, trusted, header)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || gotHeader != "" || gotContext {
			t.Fatalf("status=%d header=%q context=%v", rec.Code, gotHeader, gotContext)
		}
	})
}

func TestTrustedHTTPInvocationBodyReadIsBounded(t *testing.T) {
	setTypedProxyEnvironment(t)
	body := bytes.Repeat([]byte("x"), maxInvocationBodyBytes+1)
	trusted, header := invocationWire(t, http.MethodPost, "/public/large", body)
	reached := false
	handler := requireProxy(false)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	req := httptest.NewRequest(http.MethodPost, "/public/large", bytes.NewReader(body))
	req.Header.Set(HeaderPlatformToken, testInvocationPlatformToken)
	addInvocationWire(req, trusted, header)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || reached || rec.Body.String() != `{"error":"invalid invocation context"}`+"\n" {
		t.Fatalf("status=%d reached=%v body=%q", rec.Code, reached, rec.Body.String())
	}
}

func TestRequireProxyLegacyFallbackRemainsCompatible(t *testing.T) {
	setTypedProxyEnvironment(t)
	var got *Identity
	var rawAppID string
	handler := requireProxy(false)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = Get(r.Context())
		rawAppID = r.Header.Get(HeaderAppID)
	}))
	req := httptest.NewRequest(http.MethodGet, "/public/legacy", nil)
	req.Header.Set(HeaderPlatformToken, testInvocationPlatformToken)
	req.Header.Set(HeaderUserID, "legacy-user")
	req.Header.Set(HeaderAppID, "legacy-app")
	req.Header.Set(HeaderAppRole, RoleViewer)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || got == nil || got.AppID != "legacy-app" || rawAppID != "legacy-app" {
		t.Fatalf("status=%d identity=%+v rawAppID=%q", rec.Code, got, rawAppID)
	}
}
