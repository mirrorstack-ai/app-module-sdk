package httputil

import (
	"encoding/json"
	"log"
	"net/http"
)

// ErrorResponse is the standard JSON error envelope used across the SDK.
//
// Code is an optional stable, machine-readable identifier (e.g. "not_proxied")
// the platform/dispatch can match on without parsing the human Error string.
// It is omitted when empty so existing single-field {"error": ...} responses
// are byte-for-byte unchanged.
type ErrorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

// JSON writes v as JSON with the given status code.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("mirrorstack: httputil.JSON encode error: %v", err)
	}
}
