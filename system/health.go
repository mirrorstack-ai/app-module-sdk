package system

import (
	"net/http"

	"github.com/mirrorstack-ai/app-module-sdk/internal/httputil"
)

type healthResponse struct {
	Status string `json:"status"`
}

// Health handles GET /__mirrorstack/health.
func Health(w http.ResponseWriter, r *http.Request) {
	httputil.JSON(w, http.StatusOK, healthResponse{Status: "ok"})
}
