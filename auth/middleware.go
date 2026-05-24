package auth

import (
	"crypto/subtle"
	"log"
	"net/http"
	"os"

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
// Reads MS_INTERNAL_SECRET env var at construction time. Behavior matrix
// (see internalAuth doc).
//
// CONTRACT: the returned middleware captures the expected secret once at
// construction time. Callers (e.g., Module.internalAuth) cache and reuse
// the returned closure for every route registration. Do not add request-
// time behavior (re-reading the secret, fetching from a config service)
// without updating all cache sites in mirrorstack.go.
func InternalAuth() func(http.Handler) http.Handler {
	return internalAuth(lambdaenv.IsSet())
}

// internalAuth is the test seam; inLambda is injected so tests don't mutate
// process env captured at package init.
//
// Behavior matrix (secret = MS_INTERNAL_SECRET env var):
//
//	inLambda + secret unset → 503 (operator misconfig; platform alerting)
//	inLambda + secret set   → enforce X-MS-Internal-Secret header
//	local    + secret unset → BYPASS (local-only; `mirrorstack dev` ergonomics)
//	local    + secret set   → enforce (e.g. `mirrorstack dev --tunnel` exposes
//	                          localhost to the platform, so the secret must
//	                          be present and validated)
//
// The local-bypass lets a developer running `mirrorstack dev` curl
// /__mirrorstack/platform/manifest directly without exporting the secret —
// while still preventing tunnel mode from accidentally accepting
// unauthenticated traffic forwarded by dispatch (the CLI sets the secret
// when tunneling).
func internalAuth(inLambda bool) func(http.Handler) http.Handler {
	expected := os.Getenv("MS_INTERNAL_SECRET")
	if expected == "" {
		if inLambda {
			// SECURITY: never echo the `expected` value (or any prefix/suffix of it)
			// in this log line — Lambda log output goes to CloudWatch which is
			// commonly accessible to many roles in the org.
			log.Printf("mirrorstack: WARNING — MS_INTERNAL_SECRET not set, all internal routes will be rejected")
		} else {
			log.Printf("mirrorstack: MS_INTERNAL_SECRET not set; internal routes bypass auth (local dev)")
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if expected == "" {
				if inLambda {
					log.Printf("mirrorstack: internal auth rejected (no secret configured) from %s %s", r.RemoteAddr, r.URL.Path)
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
				// Local dev bypass — see the doc-comment matrix above.
				next.ServeHTTP(w, r)
				return
			}

			secret := r.Header.Get("X-MS-Internal-Secret")
			if !constantTimeEqual(secret, expected) {
				// SECURITY: never log the header value; only whether it was present
				log.Printf("mirrorstack: internal auth rejected (secret mismatch, header_present=%v) from %s %s", secret != "", r.RemoteAddr, r.URL.Path)
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
