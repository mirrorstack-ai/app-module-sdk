package event_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/mirrorstack-ai/app-module-sdk/event"
	"github.com/mirrorstack-ai/app-module-sdk/handler"
)

// internalRequest sets the required internal auth header for event routes.
func internalRequest(method, path string, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(handler.HeaderAuthType, handler.AuthTypeInternal)
	return req
}

func TestRegister_CreatesRoutes(t *testing.T) {
	r := chi.NewRouter()
	r.Use(handler.ExtractContext)

	var received event.Event
	event.Register(r, map[string]event.HandlerFunc{
		"oauth.user_created": func(w http.ResponseWriter, _ *http.Request, evt event.Event) {
			received = evt
			handler.WriteJSON(w, 200, map[string]bool{"ok": true})
		},
	})

	body := `{"id":"evt-1","type":"oauth.user_created","app_id":"app-123","source":"oauth","payload":{"userId":"u1"},"timestamp":"2026-03-20T00:00:00Z"}`
	req := internalRequest("POST", "/events/oauth.user_created", body)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status: got %d, want 200", w.Code)
	}
	if received.ID != "evt-1" {
		t.Errorf("event ID: got %q, want %q", received.ID, "evt-1")
	}
	if received.Type != "oauth.user_created" {
		t.Errorf("event type: got %q, want %q", received.Type, "oauth.user_created")
	}
	if received.AppID != "app-123" {
		t.Errorf("app_id: got %q, want %q", received.AppID, "app-123")
	}
	if received.Source != "oauth" {
		t.Errorf("source: got %q, want %q", received.Source, "oauth")
	}
}

func TestRegister_MultipleHandlers(t *testing.T) {
	r := chi.NewRouter()
	r.Use(handler.ExtractContext)

	var createdCalled, deletedCalled bool
	event.Register(r, map[string]event.HandlerFunc{
		"oauth.user_created": func(w http.ResponseWriter, _ *http.Request, _ event.Event) {
			createdCalled = true
			handler.WriteJSON(w, 200, map[string]bool{"ok": true})
		},
		"oauth.user_deleted": func(w http.ResponseWriter, _ *http.Request, _ event.Event) {
			deletedCalled = true
			handler.WriteJSON(w, 200, map[string]bool{"ok": true})
		},
	})

	r.ServeHTTP(httptest.NewRecorder(), internalRequest("POST", "/events/oauth.user_created",
		`{"id":"e1","type":"oauth.user_created","app_id":"a","source":"oauth","payload":{},"timestamp":"2026-03-20T00:00:00Z"}`))
	r.ServeHTTP(httptest.NewRecorder(), internalRequest("POST", "/events/oauth.user_deleted",
		`{"id":"e2","type":"oauth.user_deleted","app_id":"a","source":"oauth","payload":{},"timestamp":"2026-03-20T00:00:00Z"}`))

	if !createdCalled {
		t.Error("user_created handler was not called")
	}
	if !deletedCalled {
		t.Error("user_deleted handler was not called")
	}
}

func TestRegister_InvalidBody_Returns400(t *testing.T) {
	r := chi.NewRouter()
	r.Use(handler.ExtractContext)

	event.Register(r, map[string]event.HandlerFunc{
		"oauth.user_created": func(_ http.ResponseWriter, _ *http.Request, _ event.Event) {
			t.Error("handler should not be called for invalid body")
		},
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, internalRequest("POST", "/events/oauth.user_created", "not json"))

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
}

func TestRegister_UnknownEvent_Returns404(t *testing.T) {
	r := chi.NewRouter()
	r.Use(handler.ExtractContext)

	event.Register(r, map[string]event.HandlerFunc{
		"oauth.user_created": func(w http.ResponseWriter, _ *http.Request, _ event.Event) {
			handler.WriteJSON(w, 200, map[string]bool{"ok": true})
		},
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, internalRequest("POST", "/events/oauth.unknown_event", "{}"))

	if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status: got %d, want 404 or 405", w.Code)
	}
}

func TestRegister_PayloadPreserved(t *testing.T) {
	r := chi.NewRouter()
	r.Use(handler.ExtractContext)

	var receivedPayload json.RawMessage
	event.Register(r, map[string]event.HandlerFunc{
		"video.transcode_done": func(w http.ResponseWriter, _ *http.Request, evt event.Event) {
			receivedPayload = evt.Payload
			handler.WriteJSON(w, 200, map[string]bool{"ok": true})
		},
	})

	body := `{"id":"e1","type":"video.transcode_done","app_id":"a","source":"video","payload":{"videoId":"v-99","status":"ready"},"timestamp":"2026-03-20T00:00:00Z"}`
	req := internalRequest("POST", "/events/video.transcode_done", body)
	r.ServeHTTP(httptest.NewRecorder(), req)

	var payload struct {
		VideoID string `json:"videoId"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal(receivedPayload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.VideoID != "v-99" {
		t.Errorf("videoId: got %q, want %q", payload.VideoID, "v-99")
	}
}

func TestRegister_ContextHeaders(t *testing.T) {
	r := chi.NewRouter()
	r.Use(handler.ExtractContext)

	var gotAppID, gotSchema string
	event.Register(r, map[string]event.HandlerFunc{
		"oauth.user_created": func(w http.ResponseWriter, r *http.Request, _ event.Event) {
			gotAppID = handler.GetAppID(r.Context())
			gotSchema = handler.GetSchemaName(r.Context())
			handler.WriteJSON(w, 200, map[string]bool{"ok": true})
		},
	})

	req := internalRequest("POST", "/events/oauth.user_created",
		`{"id":"e1","type":"oauth.user_created","app_id":"app-123","source":"oauth","payload":{},"timestamp":"2026-03-20T00:00:00Z"}`)
	req.Header.Set(handler.HeaderAppID, "app-123")
	req.Header.Set(handler.HeaderSchemaName, "app_x7k2")
	r.ServeHTTP(httptest.NewRecorder(), req)

	if gotAppID != "app-123" {
		t.Errorf("appID: got %q, want %q", gotAppID, "app-123")
	}
	if gotSchema != "app_x7k2" {
		t.Errorf("schema: got %q, want %q", gotSchema, "app_x7k2")
	}
}

func TestRegister_NonInternalAuth_Returns403(t *testing.T) {
	r := chi.NewRouter()
	r.Use(handler.ExtractContext)

	event.Register(r, map[string]event.HandlerFunc{
		"oauth.user_created": func(w http.ResponseWriter, _ *http.Request, _ event.Event) {
			t.Error("handler should not be called without internal auth")
		},
	})

	// Request without AuthType: internal
	req := httptest.NewRequest("POST", "/events/oauth.user_created", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status: got %d, want 403", w.Code)
	}
}
