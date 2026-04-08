package auth

import (
	"crypto/subtle"
	"log"
	"net/http"

	"github.com/mirrorstack-ai/app-module-sdk/internal/httputil"
	"github.com/mirrorstack-ai/app-module-sdk/internal/lambdaenv"
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
//
// When MS_INTERNAL_SECRET is unset, requests get 503 in Lambda mode (so
// platform alerting can split operator misconfig from wrong-secret events)
// and 401 otherwise (dev intentionally runs without a secret). Module.Start()
// also fail-fasts on the missing-secret case in Lambda mode, but a caller
// can still bypass Start() via Module.Router().ServeHTTP — this branch is
// the runtime safety net for that case.
func InternalAuth() func(http.Handler) http.Handler {
	return internalAuth(lambdaenv.IsSet())
}

// internalAuth is the test seam; inLambda is injected so tests don't mutate
// process env captured at package init.
func internalAuth(inLambda bool) func(http.Handler) http.Handler {
	expected := os.Getenv("MS_INTERNAL_SECRET")
	if expected == "" {
		// SECURITY: never echo the `expected` value (or any prefix/suffix of it)
		// in this log line — Lambda log output goes to CloudWatch which is
		// commonly accessible to many roles in the org.
		log.Printf("mirrorstack: WARNING — MS_INTERNAL_SECRET not set, all internal routes will be rejected")
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if expected == "" {
				if inLambda {
					// SECURITY: generic body. The detailed reason is in the
					// construction-time log line above; the 503 status itself
					// is the platform's alerting signal. We don't want to
					// confirm "this endpoint exists and the module is broken"
					// to anonymous callers reaching us pre-WAF.
					httputil.JSON(w, http.StatusServiceUnavailable, httputil.ErrorResponse{
						Error: "service unavailable",
					})
					return
				}
				httputil.JSON(w, http.StatusUnauthorized, httputil.ErrorResponse{Error: "internal authentication required"})
				return
			}

			secret := r.Header.Get("X-MS-Internal-Secret")
			if !constantTimeEqual(secret, expected) {
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

