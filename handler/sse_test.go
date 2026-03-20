package handler_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/handler"
)

func TestSSE_Headers(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/stream", nil)

	handler.NewSSE(w, r)

	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type: got %q", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control: got %q", cc)
	}
	if cn := w.Header().Get("Connection"); cn != "keep-alive" {
		t.Errorf("Connection: got %q", cn)
	}
}

func TestSSE_Send(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/stream", nil)

	sse := handler.NewSSE(w, r)
	sse.Send("progress", map[string]any{"step": 1, "status": "analyzing"})

	body := w.Body.String()
	if !strings.Contains(body, "id: 1\n") {
		t.Errorf("missing id: %q", body)
	}
	if !strings.Contains(body, "event: progress\n") {
		t.Errorf("missing event type: %q", body)
	}
	if !strings.Contains(body, `"step":1`) {
		t.Errorf("missing data: %q", body)
	}
	if !strings.HasSuffix(body, "\n\n") {
		t.Errorf("should end with double newline: %q", body)
	}
}

func TestSSE_MultipleEvents(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/stream", nil)

	sse := handler.NewSSE(w, r)
	sse.Send("progress", map[string]any{"step": 1})
	sse.Send("progress", map[string]any{"step": 2})
	sse.Send("done", map[string]any{"url": "https://example.com"})

	body := w.Body.String()
	if !strings.Contains(body, "id: 1\n") {
		t.Error("missing id 1")
	}
	if !strings.Contains(body, "id: 2\n") {
		t.Error("missing id 2")
	}
	if !strings.Contains(body, "id: 3\n") {
		t.Error("missing id 3")
	}
	if !strings.Contains(body, "event: done\n") {
		t.Error("missing done event")
	}
}

func TestSSE_IsReconnect_FirstConnection(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/stream", nil)

	sse := handler.NewSSE(w, r)

	if sse.IsReconnect() {
		t.Error("should not be reconnect on first connection")
	}
	if sse.LastEventID() != 0 {
		t.Errorf("LastEventID: got %d, want 0", sse.LastEventID())
	}
}

func TestSSE_IsReconnect_WithLastEventID(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/stream", nil)
	r.Header.Set("Last-Event-ID", "5")

	sse := handler.NewSSE(w, r)

	if !sse.IsReconnect() {
		t.Error("should be reconnect when Last-Event-ID is set")
	}
	if sse.LastEventID() != 5 {
		t.Errorf("LastEventID: got %d, want 5", sse.LastEventID())
	}
}

func TestSSE_Reconnect_IDsContinue(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/stream", nil)
	r.Header.Set("Last-Event-ID", "10")

	sse := handler.NewSSE(w, r)
	sse.Send("progress", map[string]any{"resumed": true})

	body := w.Body.String()
	// IDs should continue from 11 (10 + 1)
	if !strings.Contains(body, "id: 11\n") {
		t.Errorf("ID should continue from last: %q", body)
	}
}

func TestSSE_SendError(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/stream", nil)

	sse := handler.NewSSE(w, r)
	sse.SendError("transcode failed")

	body := w.Body.String()
	if !strings.Contains(body, "event: error\n") {
		t.Errorf("missing error event: %q", body)
	}
	if !strings.Contains(body, "transcode failed") {
		t.Errorf("missing error message: %q", body)
	}
}

func TestSSE_FullFlow(t *testing.T) {
	// Simulate a full SSE endpoint.
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse := handler.NewSSE(w, r)

		if sse.IsReconnect() {
			sse.Send("resumed", map[string]any{"from": sse.LastEventID()})
			return
		}

		sse.Send("progress", map[string]any{"status": "start"})
		sse.Send("progress", map[string]any{"status": "working"})
		sse.Send("done", map[string]any{"result": "ok"})
	})

	// First connection.
	w1 := httptest.NewRecorder()
	r1 := httptest.NewRequest("GET", "/stream", nil)
	h.ServeHTTP(w1, r1)

	body1 := w1.Body.String()
	if strings.Count(body1, "event:") != 3 {
		t.Errorf("first connection should have 3 events: %q", body1)
	}

	// Reconnect.
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest("GET", "/stream", nil)
	r2.Header.Set("Last-Event-ID", "2")
	h.ServeHTTP(w2, r2)

	body2 := w2.Body.String()
	if !strings.Contains(body2, "event: resumed\n") {
		t.Errorf("reconnect should get resumed event: %q", body2)
	}
	if !strings.Contains(body2, `"from":2`) {
		t.Errorf("should contain last event ID: %q", body2)
	}
}
