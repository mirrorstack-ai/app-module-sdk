package core

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/mirrorstack-ai/app-module-sdk/internal/actor"
	"github.com/mirrorstack-ai/app-module-sdk/internal/runtime"
)

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
