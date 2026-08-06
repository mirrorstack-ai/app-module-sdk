package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/internal/actor"
	"github.com/mirrorstack-ai/app-module-sdk/internal/runtime"
)

func TestNestedPlatformAuthCannotActivateActorDelegationOnHTTPNonPlatformSurfaces(t *testing.T) {
	const delegation = "msa1.http.payload.signature"
	seen := map[string][]string{}
	dispatch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Path] = r.Header.Values(actor.HeaderDelegation)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer dispatch.Close()

	t.Setenv("MS_DISPATCH_URL", dispatch.URL)
	t.Setenv("MS_PLATFORM_TOKEN_FILE", "")
	t.Setenv("MS_PLATFORM_TOKEN", "platform-token")
	t.Setenv("MS_INTERNAL_SECRET", "")

	m, err := New(Config{ID: "actor_http_nested_test", Name: "Actor HTTP nested test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	registerNestedPlatformCall := func(register func(func(chi.Router)), scope string) {
		call := auth.PlatformAuth()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if callErr := m.CallGet(r.Context(), "target", "/platform/"+scope, nil); callErr != nil {
				http.Error(w, callErr.Error(), http.StatusBadGateway)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		register(func(r chi.Router) { r.Get("/nested", call.ServeHTTP) })
	}
	registerNestedPlatformCall(m.Public, "public")
	registerNestedPlatformCall(m.Internal, "internal")

	for _, scope := range []string{"public", "internal"} {
		req := httptest.NewRequest(http.MethodGet, "/"+scope+"/nested", nil)
		req.Header.Set(auth.HeaderPlatformToken, "platform-token")
		req.Header.Set(auth.HeaderUserID, "u-1")
		req.Header.Set(auth.HeaderAppID, "a-1")
		req.Header.Set(auth.HeaderAppRole, auth.RoleAdmin)
		req.Header.Set(actor.HeaderDelegation, delegation)
		rec := httptest.NewRecorder()
		m.Router().ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("%s status = %d, want 204: %s", scope, rec.Code, rec.Body.String())
		}
	}

	for _, scope := range []string{"public", "internal"} {
		if got := seen["/module/target/platform/"+scope]; len(got) != 0 {
			t.Errorf("%s nested PlatformAuth forwarded %s = %q, want none", scope, actor.HeaderDelegation, got)
		}
	}
}

func TestNestedPlatformAuthCannotActivateActorDelegationOnLambdaNonPlatformSurfaces(t *testing.T) {
	const delegation = "msa1.lambda.payload.signature"
	seen := map[string][]string{}
	dispatch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Path] = r.Header.Values(actor.HeaderDelegation)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer dispatch.Close()

	t.Setenv("MS_DISPATCH_URL", dispatch.URL)
	t.Setenv("MS_PLATFORM_TOKEN_FILE", "")
	t.Setenv("MS_PLATFORM_TOKEN", "platform-token")
	t.Setenv("MS_INTERNAL_SECRET", "")

	m, err := New(Config{ID: "actor_lambda_nested_test", Name: "Actor Lambda nested test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	registerNestedPlatformCall := func(register func(func(chi.Router)), scope string) {
		call := auth.PlatformAuth()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if callErr := m.CallGet(r.Context(), "target", "/platform/"+scope, nil); callErr != nil {
				http.Error(w, callErr.Error(), http.StatusBadGateway)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		register(func(r chi.Router) { r.Get("/nested", call.ServeHTTP) })
	}
	registerNestedPlatformCall(m.Public, "public")
	registerNestedPlatformCall(m.Internal, "internal")

	invoke := runtime.NewLambdaHandler(m.Router())
	for _, scope := range []string{"public", "internal"} {
		payload, marshalErr := json.Marshal(runtime.LambdaRequest{
			Method:          http.MethodGet,
			Path:            "/" + scope + "/nested",
			UserID:          "u-1",
			AppID:           "a-1",
			AppRole:         auth.RoleAdmin,
			ActorDelegation: delegation,
		})
		if marshalErr != nil {
			t.Fatalf("marshal %s: %v", scope, marshalErr)
		}
		resp, invokeErr := invoke(context.Background(), payload)
		if invokeErr != nil {
			t.Fatalf("invoke %s: %v", scope, invokeErr)
		}
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("invoke %s status = %d, want 204: %s", scope, resp.StatusCode, resp.Body)
		}
	}

	for _, scope := range []string{"public", "internal"} {
		if got := seen["/module/target/platform/"+scope]; len(got) != 0 {
			t.Errorf("%s nested PlatformAuth forwarded %s = %q, want none", scope, actor.HeaderDelegation, got)
		}
	}
}

func TestLambdaActorDelegationActivatesOnlyOnPlatformSurface(t *testing.T) {
	t.Setenv("MS_PLATFORM_TOKEN_FILE", "")
	t.Setenv("MS_PLATFORM_TOKEN", "")
	t.Setenv("MS_INTERNAL_SECRET", "")

	m, err := New(Config{ID: "actor_scope_test", Name: "Actor scope test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	type observation struct{ actor, header string }
	seen := map[string]observation{}
	handler := func(scope string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			seen[scope] = observation{
				actor:  actor.Delegation(r.Context()),
				header: r.Header.Get(actor.HeaderDelegation),
			}
			w.WriteHeader(http.StatusNoContent)
		}
	}
	m.Public(func(r chi.Router) { r.Get("/actor", handler("public")) })
	m.Platform(func(r chi.Router) { r.Get("/actor", handler("platform")) })
	m.Internal(func(r chi.Router) { r.Get("/actor", handler("internal")) })

	invoke := runtime.NewLambdaHandler(m.Router())
	for _, scope := range []string{"public", "platform", "internal"} {
		payload, marshalErr := json.Marshal(runtime.LambdaRequest{
			Method:          http.MethodGet,
			Path:            "/" + scope + "/actor",
			UserID:          "u-1",
			AppID:           "a-1",
			AppRole:         "admin",
			ActorDelegation: "msa1.payload.signature",
			Headers: map[string]string{
				actor.HeaderDelegation: "msa1.raw-spoof.signature",
			},
		})
		if marshalErr != nil {
			t.Fatalf("marshal %s: %v", scope, marshalErr)
		}
		resp, invokeErr := invoke(context.Background(), payload)
		if invokeErr != nil {
			t.Fatalf("invoke %s: %v", scope, invokeErr)
		}
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("invoke %s status = %d, want 204: %s", scope, resp.StatusCode, resp.Body)
		}
	}

	if got := seen["platform"]; got.actor != "msa1.payload.signature" || got.header != "" {
		t.Errorf("platform actor/header = %q/%q, want typed actor and no raw header", got.actor, got.header)
	}
	for _, scope := range []string{"public", "internal"} {
		if got := seen[scope]; got.actor != "" || got.header != "" {
			t.Errorf("%s actor/header = %q/%q, want both empty", scope, got.actor, got.header)
		}
	}
}
