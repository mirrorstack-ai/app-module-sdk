package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/cache"
	"github.com/mirrorstack-ai/app-module-sdk/db"
)

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	return b
}

func requireNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewLambdaHandler(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/items", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"ok": "true"})
	})

	handler := NewLambdaHandler(r)
	payload := mustMarshal(t, LambdaRequest{
		Method: "GET",
		Path:   "/items",
	})

	resp, err := handler(context.Background(), payload)
	requireNoErr(t, err)

	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestNewLambdaHandler_InvalidPayload(t *testing.T) {
	handler := NewLambdaHandler(chi.NewRouter())

	resp, err := handler(context.Background(), json.RawMessage(`not json`))
	requireNoErr(t, err)

	if resp.StatusCode != 400 {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestNewLambdaHandler_POST(t *testing.T) {
	r := chi.NewRouter()
	r.Post("/items", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(body)
	})

	handler := NewLambdaHandler(r)
	payload := mustMarshal(t, LambdaRequest{
		Method:  "POST",
		Path:    "/items",
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    `{"title":"test"}`,
	})

	resp, err := handler(context.Background(), payload)
	requireNoErr(t, err)

	if resp.StatusCode != 201 {
		t.Errorf("expected 201, got %d", resp.StatusCode)
	}
}

func TestNewLambdaHandler_QueryString(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/items", func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		json.NewEncoder(w).Encode(map[string]string{"page": page})
	})

	handler := NewLambdaHandler(r)
	payload := mustMarshal(t, LambdaRequest{
		Method: "GET",
		Path:   "/items?page=2",
	})

	resp, err := handler(context.Background(), payload)
	requireNoErr(t, err)

	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestNewLambdaHandler_TypedFieldsInjected(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/check", func(w http.ResponseWriter, r *http.Request) {
		a := auth.Get(r.Context())
		result := map[string]string{"schema": db.SchemaFrom(r.Context())}
		if a != nil {
			result["userId"] = a.UserID
			result["appId"] = a.AppID
			result["appRole"] = a.AppRole
		}
		json.NewEncoder(w).Encode(result)
	})

	handler := NewLambdaHandler(r)
	payload := mustMarshal(t, LambdaRequest{
		Method:    "GET",
		Path:      "/check",
		UserID:    "user-abc",
		AppID:     "app-xyz",
		AppRole:   "admin",
		AppSchema: "app_xyz789",
	})

	resp, err := handler(context.Background(), payload)
	requireNoErr(t, err)

	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestNewLambdaHandler_StripXMSHeaders(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/check", func(w http.ResponseWriter, r *http.Request) {
		// X-MS-* headers from the Headers map should be stripped
		spoofed := r.Header.Get("X-MS-App-Role")
		legit := r.Header.Get("Content-Type")
		json.NewEncoder(w).Encode(map[string]string{
			"spoofed": spoofed,
			"legit":   legit,
		})
	})

	handler := NewLambdaHandler(r)
	payload := mustMarshal(t, LambdaRequest{
		Method: "GET",
		Path:   "/check",
		Headers: map[string]string{
			"X-MS-App-Role": "admin",      // spoofed — should be stripped
			"x-ms-user-id":  "fake-user",  // case-insensitive — should be stripped
			"Content-Type":  "text/plain", // legit — should pass through
		},
	})

	resp, err := handler(context.Background(), payload)
	requireNoErr(t, err)

	var body map[string]string
	json.Unmarshal([]byte(resp.Body), &body)

	if body["spoofed"] != "" {
		t.Errorf("X-MS-App-Role header should be stripped, got %q", body["spoofed"])
	}
	if body["legit"] != "text/plain" {
		t.Errorf("Content-Type should pass through, got %q", body["legit"])
	}
}

// TestNewLambdaHandler_AuthSecretHeadersSurvive pins the prod-transport
// contract (decisions/09 §4): the platform-auth SECRET headers must reach the
// module router so internalAuth / RequireProxy can validate them on the Lambda
// path, while identity-CLAIM headers stay stripped (identity rides typed fields).
func TestNewLambdaHandler_AuthSecretHeadersSurvive(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/check", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"internalSecret": r.Header.Get(auth.HeaderInternalSecret),
			"platformToken":  r.Header.Get(auth.HeaderPlatformToken),
			"userID":         r.Header.Get(auth.HeaderUserID),
			"appID":          r.Header.Get(auth.HeaderAppID),
			"appRole":        r.Header.Get(auth.HeaderAppRole),
		})
	})

	handler := NewLambdaHandler(r)
	payload := mustMarshal(t, LambdaRequest{
		Method: "GET",
		Path:   "/check",
		Headers: map[string]string{
			auth.HeaderInternalSecret: "platform-secret", // exempt — must survive
			auth.HeaderPlatformToken:  "platform-token",   // exempt — must survive
			auth.HeaderUserID:         "spoofed-user",      // claim — must be stripped
			auth.HeaderAppID:          "spoofed-app",       // claim — must be stripped
			auth.HeaderAppRole:        "admin",             // claim — must be stripped
		},
	})

	resp, err := handler(context.Background(), payload)
	requireNoErr(t, err)

	var body map[string]string
	if err := json.Unmarshal([]byte(resp.Body), &body); err != nil {
		t.Fatalf("unmarshal handler response: %v", err)
	}

	if body["internalSecret"] != "platform-secret" {
		t.Errorf("%s must survive the strip, got %q", auth.HeaderInternalSecret, body["internalSecret"])
	}
	if body["platformToken"] != "platform-token" {
		t.Errorf("%s must survive the strip, got %q", auth.HeaderPlatformToken, body["platformToken"])
	}
	for _, claim := range []string{"userID", "appID", "appRole"} {
		if body[claim] != "" {
			t.Errorf("identity claim %q must be stripped, got %q", claim, body[claim])
		}
	}
}

// TestMsAuthSecretHeadersMatchConstants structurally pins the exempt-header map
// to the auth package constants. The behavioral test above sends whatever the
// constants say, so a rename of a constant WITHOUT updating the map would slip
// past it; this test fails closed on that drift.
func TestMsAuthSecretHeadersMatchConstants(t *testing.T) {
	for _, h := range []string{auth.HeaderInternalSecret, auth.HeaderPlatformToken} {
		if !msAuthSecretHeaders[strings.ToLower(h)] {
			t.Errorf("auth secret header %q is missing from the msAuthSecretHeaders exempt set", h)
		}
	}
	for _, h := range []string{auth.HeaderUserID, auth.HeaderAppID, auth.HeaderAppRole} {
		if msAuthSecretHeaders[strings.ToLower(h)] {
			t.Errorf("identity claim %q must NOT be exempt from the strip", h)
		}
	}
}

// TestNewLambdaHandler_MarksPayloadTrusted pins that NewLambdaHandler is the
// writer of the payload-trust mark: every request it builds carries it, so
// auth.RequireProxy passes shim-delivered requests the way it passes Lambda.
func TestNewLambdaHandler_MarksPayloadTrusted(t *testing.T) {
	r := chi.NewRouter()
	var trusted bool
	r.Get("/check", func(w http.ResponseWriter, r *http.Request) {
		trusted = auth.PayloadTrusted(r.Context())
	})

	handler := NewLambdaHandler(r)
	resp, err := handler(context.Background(), mustMarshal(t, LambdaRequest{
		Method: "GET",
		Path:   "/check",
	}))
	requireNoErr(t, err)

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !trusted {
		t.Error("synthetic request context must carry the payload-trust mark")
	}
}

func TestNewLambdaHandler_InvalidSchema(t *testing.T) {
	handler := NewLambdaHandler(chi.NewRouter())
	payload := mustMarshal(t, LambdaRequest{
		Method:    "GET",
		Path:      "/items",
		AppSchema: `app"; DROP TABLE users;--`,
	})

	resp, err := handler(context.Background(), payload)
	requireNoErr(t, err)

	if resp.StatusCode != 400 {
		t.Errorf("expected 400 for invalid schema, got %d", resp.StatusCode)
	}
}

func TestNewLambdaHandler_EmptySchema(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/items", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := NewLambdaHandler(r)
	payload := mustMarshal(t, LambdaRequest{
		Method:    "GET",
		Path:      "/items",
		AppSchema: "", // empty is OK (dev mode)
	})

	resp, err := handler(context.Background(), payload)
	requireNoErr(t, err)

	if resp.StatusCode != 200 {
		t.Errorf("expected 200 for empty schema, got %d", resp.StatusCode)
	}
}

func TestNewLambdaHandler_ResourcesInjected(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/check", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		dbCred := db.CredentialFrom(ctx)
		cacheCred := cache.CredentialFrom(ctx)
		result := map[string]string{}
		if dbCred != nil {
			result["dbUser"] = dbCred.Username
		}
		if cacheCred != nil {
			result["cacheUser"] = cacheCred.Username
		}
		json.NewEncoder(w).Encode(result)
	})

	handler := NewLambdaHandler(r)
	payload := mustMarshal(t, LambdaRequest{
		Method: "GET",
		Path:   "/check",
		Resources: &Resources{
			DB:    &db.Credential{Username: "mod_media__app_abc"},
			Cache: &cache.Credential{Endpoint: "localhost:6379", Username: "mod_media"},
		},
	})

	resp, err := handler(context.Background(), payload)
	requireNoErr(t, err)

	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]string
	json.Unmarshal([]byte(resp.Body), &body)

	if body["dbUser"] != "mod_media__app_abc" {
		t.Errorf("expected dbUser 'mod_media__app_abc', got %q", body["dbUser"])
	}
	if body["cacheUser"] != "mod_media" {
		t.Errorf("expected cacheUser 'mod_media', got %q", body["cacheUser"])
	}
}
