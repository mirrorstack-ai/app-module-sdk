package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
)

const MaxBodySize = 1 << 20 // 1 MB

var bufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

type apiError struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)

	if err := json.NewEncoder(buf).Encode(v); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"code":"internal_error","message":"internal server error"}}`)) //nolint:errcheck
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	buf.WriteTo(w) //nolint:errcheck
}

func DecodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodySize)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		BadRequest(w, "invalid request body")
		return err
	}
	if dec.More() {
		BadRequest(w, "invalid request body")
		return errors.New("request body contains trailing data")
	}
	return nil
}

func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, apiError{Error: errorBody{Code: code, Message: message}})
}

func BadRequest(w http.ResponseWriter, message string) {
	WriteError(w, http.StatusBadRequest, "bad_request", message)
}
func Unauthorized(w http.ResponseWriter, message string) {
	WriteError(w, http.StatusUnauthorized, "unauthorized", message)
}
func Forbidden(w http.ResponseWriter, message string) {
	WriteError(w, http.StatusForbidden, "forbidden", message)
}
func NotFound(w http.ResponseWriter, message string) {
	WriteError(w, http.StatusNotFound, "not_found", message)
}
func Conflict(w http.ResponseWriter, message string) {
	WriteError(w, http.StatusConflict, "conflict", message)
}

func InternalError(w http.ResponseWriter) {
	WriteError(w, http.StatusInternalServerError, "internal_error", "internal server error")
}

func ServiceUnavailable(w http.ResponseWriter, message string) {
	WriteError(w, http.StatusServiceUnavailable, "service_unavailable", message)
}

func ReadBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		BadRequest(w, "request body too large")
		return nil, err
	}
	return body, nil
}
