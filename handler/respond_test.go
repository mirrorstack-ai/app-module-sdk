package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/handler"
)

// --- WriteJSON ---

func TestWriteJSON_Success(t *testing.T) {
	w := httptest.NewRecorder()
	handler.WriteJSON(w, http.StatusOK, map[string]string{"name": "test"})

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusOK)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type: got %q, want %q", ct, "application/json")
	}

	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["name"] != "test" {
		t.Errorf("body.name: got %q, want %q", body["name"], "test")
	}
}

func TestWriteJSON_CustomStatus(t *testing.T) {
	w := httptest.NewRecorder()
	handler.WriteJSON(w, http.StatusCreated, map[string]int{"id": 1})

	if w.Code != http.StatusCreated {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusCreated)
	}
}

func TestWriteJSON_UnserializableValue_Returns500(t *testing.T) {
	w := httptest.NewRecorder()
	handler.WriteJSON(w, http.StatusOK, make(chan int)) // channels can't be JSON encoded

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusInternalServerError)
	}

	var body errorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body.Error.Code != "internal_error" {
		t.Errorf("error code: got %q, want %q", body.Error.Code, "internal_error")
	}
}

func TestWriteJSON_NilValue(t *testing.T) {
	w := httptest.NewRecorder()
	handler.WriteJSON(w, http.StatusOK, nil)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusOK)
	}
}

// --- DecodeJSON ---

func TestDecodeJSON_ValidJSON(t *testing.T) {
	body := `{"title":"test video","count":5}`
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	w := httptest.NewRecorder()

	var req struct {
		Title string `json:"title"`
		Count int    `json:"count"`
	}
	err := handler.DecodeJSON(w, r, &req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Title != "test video" {
		t.Errorf("title: got %q, want %q", req.Title, "test video")
	}
	if req.Count != 5 {
		t.Errorf("count: got %d, want %d", req.Count, 5)
	}
}

func TestDecodeJSON_InvalidJSON_Returns400(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader("not json"))
	w := httptest.NewRecorder()

	var req struct{}
	err := handler.DecodeJSON(w, r, &req)

	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusBadRequest)
	}

	// Should NOT leak Go type names
	var body errorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body.Error.Message != "invalid request body" {
		t.Errorf("message: got %q, want %q", body.Error.Message, "invalid request body")
	}
}

func TestDecodeJSON_EmptyBody_Returns400(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader(""))
	w := httptest.NewRecorder()

	var req struct{}
	err := handler.DecodeJSON(w, r, &req)

	if err == nil {
		t.Fatal("expected error for empty body")
	}
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestDecodeJSON_OversizedBody_Returns400(t *testing.T) {
	bigBody := strings.Repeat("a", handler.MaxBodySize+100)
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"data":"`+bigBody+`"}`))
	w := httptest.NewRecorder()

	var req struct{}
	err := handler.DecodeJSON(w, r, &req)

	if err == nil {
		t.Fatal("expected error for oversized body")
	}
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestDecodeJSON_TrailingData_Returns400(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"ok"}{"injected":true}`))
	w := httptest.NewRecorder()

	var req struct{ Name string }
	err := handler.DecodeJSON(w, r, &req)

	if err == nil {
		t.Fatal("expected error for trailing data")
	}
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// --- Error helpers ---

func TestBadRequest(t *testing.T) {
	w := httptest.NewRecorder()
	handler.BadRequest(w, "missing title")
	assertError(t, w, http.StatusBadRequest, "bad_request", "missing title")
}

func TestUnauthorized(t *testing.T) {
	w := httptest.NewRecorder()
	handler.Unauthorized(w, "token expired")
	assertError(t, w, http.StatusUnauthorized, "unauthorized", "token expired")
}

func TestForbidden(t *testing.T) {
	w := httptest.NewRecorder()
	handler.Forbidden(w, "admin only")
	assertError(t, w, http.StatusForbidden, "forbidden", "admin only")
}

func TestNotFound(t *testing.T) {
	w := httptest.NewRecorder()
	handler.NotFound(w, "video not found")
	assertError(t, w, http.StatusNotFound, "not_found", "video not found")
}

func TestConflict(t *testing.T) {
	w := httptest.NewRecorder()
	handler.Conflict(w, "already exists")
	assertError(t, w, http.StatusConflict, "conflict", "already exists")
}

func TestInternalError(t *testing.T) {
	w := httptest.NewRecorder()
	handler.InternalError(w)
	assertError(t, w, http.StatusInternalServerError, "internal_error", "internal server error")
}

func TestServiceUnavailable(t *testing.T) {
	w := httptest.NewRecorder()
	handler.ServiceUnavailable(w, "database unreachable")
	assertError(t, w, http.StatusServiceUnavailable, "service_unavailable", "database unreachable")
}

// --- ReadBody ---

func TestReadBody_Success(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader("hello world"))
	w := httptest.NewRecorder()

	body, err := handler.ReadBody(w, r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(body) != "hello world" {
		t.Errorf("body: got %q, want %q", string(body), "hello world")
	}
}

func TestReadBody_OversizedBody_ReturnsError(t *testing.T) {
	bigBody := strings.Repeat("x", handler.MaxBodySize+100)
	r := httptest.NewRequest("POST", "/", strings.NewReader(bigBody))
	w := httptest.NewRecorder()

	_, err := handler.ReadBody(w, r)
	if err == nil {
		t.Fatal("expected error for oversized body")
	}
}

// --- Helpers ---

func assertError(t *testing.T, w *httptest.ResponseRecorder, status int, code, message string) {
	t.Helper()
	if w.Code != status {
		t.Errorf("status: got %d, want %d", w.Code, status)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type: got %q, want %q", ct, "application/json")
	}
	var body errorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body.Error.Code != code {
		t.Errorf("error code: got %q, want %q", body.Error.Code, code)
	}
	if body.Error.Message != message {
		t.Errorf("error message: got %q, want %q", body.Error.Message, message)
	}
}
