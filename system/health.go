package system

import "net/http"

type healthResponse struct {
	Status string `json:"status"`
}

// Health handles GET /__mirrorstack/health.
func Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}
