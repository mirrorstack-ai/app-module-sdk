package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
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
		appID := r.Header.Get("X-MS-App-ID")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"app": appID})
	})

	handler := NewLambdaHandler(r)
	payload := mustMarshal(t, LambdaRequest{
		Method:  "GET",
		Path:    "/items",
		Headers: map[string]string{"X-MS-App-ID": "test-app-123"},
	})

	resp, err := handler(context.Background(), payload)
	requireNoErr(t, err)

	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if resp.Headers["Content-Type"] == nil || resp.Headers["Content-Type"][0] != "application/json" {
		t.Errorf("unexpected content-type: %v", resp.Headers["Content-Type"])
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

func TestNewLambdaHandler_EmptyMethod(t *testing.T) {
	// Go's http.NewRequestWithContext treats empty method as GET
	r := chi.NewRouter()
	r.Get("/items", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := NewLambdaHandler(r)
	payload := mustMarshal(t, LambdaRequest{Path: "/items"})

	resp, err := handler(context.Background(), payload)
	requireNoErr(t, err)

	if resp.StatusCode != 200 {
		t.Errorf("expected 200 for empty method (defaults to GET), got %d", resp.StatusCode)
	}
}
