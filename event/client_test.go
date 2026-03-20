package event_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/mirrorstack-ai/app-module-sdk/event"
	"github.com/mirrorstack-ai/app-module-sdk/handler"
)

// --- Mock Lambda ---

type mockLambda struct {
	gotInput *lambda.InvokeInput
	output   *lambda.InvokeOutput
	err      error
}

func (m *mockLambda) Invoke(_ context.Context, input *lambda.InvokeInput, _ ...func(*lambda.Options)) (*lambda.InvokeOutput, error) {
	m.gotInput = input
	if m.err != nil {
		return nil, m.err
	}
	if m.output != nil {
		return m.output, nil
	}
	return &lambda.InvokeOutput{}, nil
}

// --- HTTPClient (local dev) ---

func TestHTTPClient_Emit_FireAndForget(t *testing.T) {
	var called atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called.Add(1)
		w.WriteHeader(200)
	}))
	defer server.Close()

	client := event.NewHTTPClient(server.URL, "dev-secret", "video")
	ctx := contextWithHeaders(t, map[string]string{
		handler.HeaderAppID:      "app-local",
		handler.HeaderSchemaName: "app_dev",
	})

	err := client.Emit(ctx, "transcode_completed", map[string]any{"videoId": "v-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Emit is async (fire-and-forget) — wait briefly for the goroutine.
	time.Sleep(100 * time.Millisecond)

	if called.Load() != 1 {
		t.Errorf("expected server to be called once, got %d", called.Load())
	}
}

func TestHTTPClient_Call(t *testing.T) {
	var gotPath, gotSecret string
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotSecret = r.Header.Get(handler.HeaderInternalSecret)
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true,"newBalance":50}`)) //nolint:errcheck
	}))
	defer server.Close()

	client := event.NewHTTPClient(server.URL, "dev-secret", "video")
	ctx := contextWithHeaders(t, map[string]string{
		handler.HeaderAppID:      "app-local",
		handler.HeaderSchemaName: "app_dev",
	})

	var resp struct {
		Success    bool `json:"success"`
		NewBalance int  `json:"newBalance"`
	}
	err := client.Call(ctx, "credit", "v1", "/deduct", map[string]any{"amount": 10}, &resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotPath != "/internal/invoke" {
		t.Errorf("path: got %q, want %q", gotPath, "/internal/invoke")
	}
	if gotSecret != "dev-secret" {
		t.Errorf("secret: got %q, want %q", gotSecret, "dev-secret")
	}
	if !resp.Success {
		t.Error("expected success=true")
	}
	if resp.NewBalance != 50 {
		t.Errorf("newBalance: got %d, want 50", resp.NewBalance)
	}
	if gotBody["action"] != "call" {
		t.Errorf("action: got %v, want %q", gotBody["action"], "call")
	}
}

func TestHTTPClient_Call_ServerError_ReturnsCallError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"error":"internal"}`)) //nolint:errcheck
	}))
	defer server.Close()

	client := event.NewHTTPClient(server.URL, "dev-secret", "video")
	ctx := contextWithHeaders(t, map[string]string{handler.HeaderAppID: "app-1", handler.HeaderSchemaName: "app_test"})

	var resp struct{}
	err := client.Call(ctx, "credit", "v1", "/fail", nil, &resp)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}

	var callErr *event.CallError
	if !errors.As(err, &callErr) {
		t.Fatalf("expected *CallError, got %T: %v", err, err)
	}
}

func TestHTTPClient_Unreachable_ReturnsError(t *testing.T) {
	client := event.NewHTTPClient("http://127.0.0.1:1", "secret", "video")
	ctx := contextWithHeaders(t, map[string]string{handler.HeaderAppID: "app-1", handler.HeaderSchemaName: "app_test"})

	err := client.Call(ctx, "credit", "v1", "/test", nil, nil)
	if err == nil {
		t.Fatal("expected error for unreachable server")
	}
}

// --- Helpers ---

func contextWithHeaders(t *testing.T, headers map[string]string) context.Context {
	t.Helper()
	r := httptest.NewRequest("GET", "/", nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	var captured context.Context
	handler.ExtractContext(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		captured = r.Context()
	})).ServeHTTP(httptest.NewRecorder(), r)
	if captured == nil {
		t.Fatal("ExtractContext did not call next handler")
	}
	return captured
}
