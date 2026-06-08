package mirrorstack_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	ms "github.com/mirrorstack-ai/app-module-sdk"
)

func TestWriteJSON(t *testing.T) {
	t.Run("encodes body with status + content-type", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ms.WriteJSON(rec, http.StatusCreated, map[string]string{"hello": "world"})
		if rec.Code != http.StatusCreated {
			t.Errorf("status = %d, want 201", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q, want application/json", ct)
		}
		var got map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("body not JSON: %v", err)
		}
		if got["hello"] != "world" {
			t.Errorf("body = %v", got)
		}
	})

	t.Run("nil body writes status + header, no body", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ms.WriteJSON(rec, http.StatusNoContent, nil)
		if rec.Code != http.StatusNoContent {
			t.Errorf("status = %d, want 204", rec.Code)
		}
		if rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("content-type not set on empty body")
		}
		if rec.Body.Len() != 0 {
			t.Errorf("expected empty body, got %q", rec.Body.String())
		}
	})
}

func TestWriteError(t *testing.T) {
	rec := httptest.NewRecorder()
	ms.WriteError(rec, http.StatusBadRequest, "nope")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	var got ms.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body not an ErrorResponse: %v", err)
	}
	if got.Error != "nope" {
		t.Errorf("error = %q, want nope", got.Error)
	}
	// Wire shape must be exactly {"error": "..."} — match the SDK middleware envelope.
	if s := rec.Body.String(); s != "{\"error\":\"nope\"}\n" {
		t.Errorf("wire shape = %q, want {\"error\":\"nope\"}\\n", s)
	}
}
