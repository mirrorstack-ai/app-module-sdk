package auth

import (
	"crypto/subtle"
	"net/http"
	"os"

	"github.com/mirrorstack-ai/app-module-sdk/internal/httputil"
)

type errorResponse struct {
	Error string `json:"error"`
}

// PlatformAuth returns middleware that requires an admin role by default.
// Use RequirePermission for routes that allow member or viewer access.
func PlatformAuth() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role := AppRole(r.Context())
			if role == "" {
				httputil.JSON(w, http.StatusUnauthorized, errorResponse{Error: "authentication required"})
				return
			}
			if role != RoleAdmin {
				httputil.JSON(w, http.StatusForbidden, errorResponse{Error: "admin access required"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// PublicAuth returns middleware that allows any request.
// Identity is still available in context if present (for logging).
func PublicAuth() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return next // pass-through
	}
}

// InternalAuth returns middleware that validates the internal secret.
// Reads MS_INTERNAL_SECRET env var at construction time.
// Uses constant-time comparison to prevent timing attacks.
func InternalAuth() func(http.Handler) http.Handler {
	expected := os.Getenv("MS_INTERNAL_SECRET")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			secret := r.Header.Get("X-MS-Internal-Secret")

			if expected == "" || !constantTimeEqual(secret, expected) {
				// Identical message for missing and wrong secret
				httputil.JSON(w, http.StatusUnauthorized, errorResponse{Error: "internal authentication required"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
