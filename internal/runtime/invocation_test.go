package runtime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/actor"
	"github.com/mirrorstack-ai/app-module-sdk/internal/invocationwire"
	"github.com/mirrorstack-ai/app-module-sdk/invocation"
)

const (
	lambdaInvocationModuleID   = "m11111111111141118111111111111111"
	lambdaInvocationModuleSlug = "user-core"
)

func lambdaInvocationWire(t *testing.T, method, path string, body []byte) (invocation.Context, json.RawMessage) {
	t.Helper()
	raw, err := os.ReadFile("../../invocation/testdata/invocation_v1.json")
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
	return trusted, json.RawMessage(raw)
}

func lambdaLegacyHeaders(trusted invocation.Context) map[string]string {
	return map[string]string{
		invocationwire.LegacyIdentityKindHeader:     trusted.Identity.Kind,
		invocationwire.LegacyAllowedRedirectsHeader: strings.Join(trusted.Routes.Redirects, ","),
		invocationwire.LegacyRequestIDHeader:        trusted.Request.ID,
	}
}

func compatibleLambdaRequest(trusted invocation.Context, raw json.RawMessage, body []byte) LambdaRequest {
	return LambdaRequest{
		Method:          trusted.Request.Method,
		Path:            trusted.Request.Path,
		Headers:         lambdaLegacyHeaders(trusted),
		Body:            string(body),
		Invocation:      raw,
		UserID:          trusted.Identity.UserID,
		AppID:           trusted.App.ID,
		AppRole:         trusted.Identity.AppRole,
		AppSchema:       trusted.App.Schema,
		ActorDelegation: trusted.Identity.ActorDelegation,
	}
}

func TestLambdaInvocationInstallsTypedContextAndStripsCompatibilityWire(t *testing.T) {
	body := []byte(`{"title":"typed"}`)
	trusted, raw := lambdaInvocationWire(t, http.MethodPost, "/platform/items?q=1", body)
	req := compatibleLambdaRequest(trusted, raw, body)
	req.Headers["Content-Type"] = "application/json"

	reached := false
	handler := NewLambdaHandlerWithTasks(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		got, ok := invocation.FromContext(r.Context())
		if !ok || got.Request.ID != trusted.Request.ID || got.Module != trusted.Module {
			t.Fatalf("typed invocation missing or changed: %+v, %v", got, ok)
		}
		identity := auth.Get(r.Context())
		if identity == nil || identity.UserID != trusted.Identity.UserID || identity.AppID != trusted.App.ID || identity.AppRole != trusted.Identity.AppRole {
			t.Fatalf("typed identity mismatch: %+v", identity)
		}
		if schema := db.SchemaFrom(r.Context()); schema != trusted.App.Schema {
			t.Fatalf("schema=%q want=%q", schema, trusted.App.Schema)
		}
		proof := invocationwire.ProofFromContext(r.Context())
		if !bytes.Equal(proof, raw) {
			t.Fatal("Lambda did not retain the exact private invocation proof")
		}
		proof[0] ^= 0xff
		if again := invocationwire.ProofFromContext(r.Context()); len(again) == 0 || again[0] == proof[0] {
			t.Fatal("private Lambda invocation proof shared mutable storage")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatal("ordinary header was removed")
		}
		for name := range r.Header {
			if isTypedInvocationWireHeader(name) {
				t.Fatalf("typed/legacy wire reached module: %s", name)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}), lambdaInvocationModuleID, lambdaInvocationModuleSlug, nil)

	response, err := handler(t.Context(), mustMarshal(t, req))
	requireNoErr(t, err)
	if response.StatusCode != http.StatusNoContent || !reached {
		t.Fatalf("response=%+v reached=%v", response, reached)
	}
}

func TestLambdaInvocationConflictsAndBindingsFailClosed(t *testing.T) {
	body := []byte("bound-body")
	trusted, raw := lambdaInvocationWire(t, http.MethodPost, "/public/check?q=1", body)

	tests := []struct {
		name       string
		moduleID   string
		moduleSlug string
		mutate     func(*LambdaRequest)
	}{
		{name: "method", moduleID: lambdaInvocationModuleID, moduleSlug: lambdaInvocationModuleSlug, mutate: func(req *LambdaRequest) { req.Method = http.MethodPut }},
		{name: "path", moduleID: lambdaInvocationModuleID, moduleSlug: lambdaInvocationModuleSlug, mutate: func(req *LambdaRequest) { req.Path = "/public/other" }},
		{name: "body", moduleID: lambdaInvocationModuleID, moduleSlug: lambdaInvocationModuleSlug, mutate: func(req *LambdaRequest) { req.Body = "different" }},
		{name: "module id", moduleID: "m99999999999949998999999999999999", moduleSlug: lambdaInvocationModuleSlug},
		{name: "noncanonical expected module id", moduleID: "legacy-module-id", moduleSlug: lambdaInvocationModuleSlug},
		{name: "module slug", moduleID: lambdaInvocationModuleID, moduleSlug: "other-module"},
		{name: "legacy user", moduleID: lambdaInvocationModuleID, moduleSlug: lambdaInvocationModuleSlug, mutate: func(req *LambdaRequest) { req.UserID = "forged-user" }},
		{name: "legacy app", moduleID: lambdaInvocationModuleID, moduleSlug: lambdaInvocationModuleSlug, mutate: func(req *LambdaRequest) { req.AppID = "forged-app" }},
		{name: "legacy role", moduleID: lambdaInvocationModuleID, moduleSlug: lambdaInvocationModuleSlug, mutate: func(req *LambdaRequest) { req.AppRole = "viewer" }},
		{name: "legacy schema", moduleID: lambdaInvocationModuleID, moduleSlug: lambdaInvocationModuleSlug, mutate: func(req *LambdaRequest) { req.AppSchema = "app_forged" }},
		{name: "legacy actor", moduleID: lambdaInvocationModuleID, moduleSlug: lambdaInvocationModuleSlug, mutate: func(req *LambdaRequest) { req.ActorDelegation = "msa1.forged.signature" }},
		{name: "legacy header", moduleID: lambdaInvocationModuleID, moduleSlug: lambdaInvocationModuleSlug, mutate: func(req *LambdaRequest) { req.Headers[invocationwire.LegacyRequestIDHeader] = "forged-request" }},
		{name: "duplicate typed header", moduleID: lambdaInvocationModuleID, moduleSlug: lambdaInvocationModuleSlug, mutate: func(req *LambdaRequest) { req.Headers[strings.ToLower(invocationwire.Header)] = "duplicate" }},
		{name: "duplicate legacy case variant", moduleID: lambdaInvocationModuleID, moduleSlug: lambdaInvocationModuleSlug, mutate: func(req *LambdaRequest) {
			req.Headers[strings.ToLower(invocationwire.LegacyRequestIDHeader)] = trusted.Request.ID
		}},
		{name: "noncanonical invocation", moduleID: lambdaInvocationModuleID, moduleSlug: lambdaInvocationModuleSlug, mutate: func(req *LambdaRequest) {
			req.Invocation = bytes.Replace(req.Invocation, []byte(`"v":1`), []byte(`"v":1,"future":true`), 1)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := compatibleLambdaRequest(trusted, append(json.RawMessage(nil), raw...), body)
			if test.mutate != nil {
				test.mutate(&req)
			}
			reached := false
			handler := NewLambdaHandlerWithTasks(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				reached = true
			}), test.moduleID, test.moduleSlug, nil)
			response, err := handler(t.Context(), mustMarshal(t, req))
			requireNoErr(t, err)
			if response.StatusCode != http.StatusBadRequest || response.Body != `{"error":"invalid invocation context"}` || reached {
				t.Fatalf("response=%+v reached=%v", response, reached)
			}
			if strings.Contains(response.Body, "forged") || strings.Contains(response.Body, trusted.App.ID) {
				t.Fatalf("error leaked invocation value: %q", response.Body)
			}
		})
	}
}

func TestLambdaTypedActorDelegationIsSurfaceBound(t *testing.T) {
	tests := []struct {
		name      string
		wrap      func(http.Handler) http.Handler
		wantActor bool
	}{
		{name: "public", wrap: func(next http.Handler) http.Handler { return auth.RequireProxy()(next) }},
		{name: "internal", wrap: func(next http.Handler) http.Handler { return auth.InternalAuth()(next) }},
		{name: "platform", wantActor: true, wrap: func(next http.Handler) http.Handler {
			chain := auth.RequireProxy()(auth.PlatformAuth()(next))
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				chain.ServeHTTP(w, r.WithContext(actor.WithPlatformSurface(r.Context())))
			})
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := "/" + test.name + "/check"
			trusted, raw := lambdaInvocationWire(t, http.MethodGet, path, nil)
			req := compatibleLambdaRequest(trusted, raw, nil)
			var gotActor, gotHeader string
			route := test.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotActor = actor.Delegation(r.Context())
				gotHeader = r.Header.Get(actor.HeaderDelegation)
				if _, ok := invocation.FromContext(r.Context()); !ok {
					t.Fatal("surface handler lost typed invocation")
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			handler := NewLambdaHandler(route)
			response, err := handler(t.Context(), mustMarshal(t, req))
			requireNoErr(t, err)
			if response.StatusCode != http.StatusNoContent || gotHeader != "" {
				t.Fatalf("response=%+v actor header=%q", response, gotHeader)
			}
			if test.wantActor && gotActor != trusted.Identity.ActorDelegation {
				t.Fatalf("actor=%q want=%q", gotActor, trusted.Identity.ActorDelegation)
			}
			if !test.wantActor && gotActor != "" {
				t.Fatalf("actor=%q want discarded", gotActor)
			}
		})
	}
}

func TestLambdaLegacyEnvelopeWithoutInvocationRemainsCompatible(t *testing.T) {
	var got *auth.Identity
	handler := NewLambdaHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = auth.Get(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	response, err := handler(t.Context(), mustMarshal(t, LambdaRequest{
		Method: "GET", Path: "/legacy", UserID: "legacy-user", AppID: "legacy-app", AppRole: auth.RoleViewer, AppSchema: "app_legacy",
	}))
	requireNoErr(t, err)
	if response.StatusCode != http.StatusNoContent || got == nil || got.AppID != "legacy-app" {
		t.Fatalf("response=%+v identity=%+v", response, got)
	}
}
