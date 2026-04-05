package httputil

import (
	"encoding/json"
	"log"
	"net/http"
)

// ErrorResponse is the standard JSON error envelope used across the SDK.
type ErrorResponse struct {
	Error string `json:"error"`
}

// JSON writes v as JSON with the given status code.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("mirrorstack: httputil.JSON encode error: %v", err)
	}
}
