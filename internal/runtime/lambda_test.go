package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/mirrorstack-ai/app-module-sdk/auth"
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
		ctx := r.Context()
		json.NewEncoder(w).Encode(map[string]string{
			"userId":  auth.UserID(ctx),
			"appId":   auth.AppID(ctx),
			"appRole": auth.AppRole(ctx),
			"schema":  db.SchemaFrom(ctx),
		})
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
			"X-MS-App-Role": "admin",       // spoofed — should be stripped
			"x-ms-user-id":  "fake-user",   // case-insensitive — should be stripped
			"Content-Type":  "text/plain",   // legit — should pass through
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
