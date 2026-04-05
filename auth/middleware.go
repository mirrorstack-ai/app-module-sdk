package auth

import (
	"crypto/subtle"
	"log"
	"net/http"
	"os"

	"github.com/mirrorstack-ai/app-module-sdk/internal/httputil"
)

// PlatformAuth returns middleware that requires an authenticated user.
// Use RequirePermission per-route for authorization (which roles can access).
func PlatformAuth() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			a := Get(r.Context())
			if a == nil || a.AppRole == "" {
				httputil.JSON(w, http.StatusUnauthorized, httputil.ErrorResponse{Error: "authentication required"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// PublicAuth returns middleware that allows any request.
func PublicAuth() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return next
	}
}

// InternalAuth returns middleware that validates the internal secret.
// Reads MS_INTERNAL_SECRET env var at construction time.
func InternalAuth() func(http.Handler) http.Handler {
	expected := os.Getenv("MS_INTERNAL_SECRET")
	if expected == "" {
		log.Printf("mirrorstack: WARNING — MS_INTERNAL_SECRET not set, all internal routes will be rejected")
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			secret := r.Header.Get("X-MS-Internal-Secret")
			if expected == "" || !constantTimeEqual(secret, expected) {
				httputil.JSON(w, http.StatusUnauthorized, httputil.ErrorResponse{Error: "internal authentication required"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
