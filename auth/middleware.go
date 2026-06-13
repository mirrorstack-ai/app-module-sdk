package auth

import (
	"crypto/subtle"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/mirrorstack-ai/app-module-sdk/internal/httputil"
	"github.com/mirrorstack-ai/app-module-sdk/internal/lambdaenv"
)

// Trusted-forwarder identity-injection headers. A caller proves it's a
// trusted forwarder (the platform or its dispatch) by sending a valid
// X-MS-Internal-Secret; if proven, PlatformAuth trusts the user identity
// those callers assert via these headers.
const (
	HeaderInternalSecret = "X-MS-Internal-Secret"
	HeaderPlatformToken  = "X-MS-Platform-Token"
	HeaderUserID         = "X-MS-User-ID"
	HeaderAppID          = "X-MS-App-ID"
	HeaderAppRole        = "X-MS-App-Role"
)

// PlatformAuth returns middleware that requires an authenticated user.
// Use RequirePermission per-route for authorization (which roles can access).
//
// Identity sources (first match wins):
//
//  1. An Identity already set in ctx (the Lambda runtime's authorizer
//     path puts one here before this middleware runs).
//  2. Trusted-forwarder injection: a request that presents a valid
//     X-MS-Internal-Secret may also assert user identity via
//     X-MS-User-ID / X-MS-App-ID / X-MS-App-Role. PlatformAuth promotes
//     those to ctx.Identity. Same trust signal InternalAuth uses.
//  3. Local-dev bypass: when MS_INTERNAL_SECRET is unset AND we are NOT
//     in Lambda, a synthetic admin identity is injected so
//     `mirrorstack dev` (no tunnel) can render platform-scope routes
//     without the developer wiring auth manually.
//
// In Lambda with no secret configured, this returns 503 (operator
// misconfiguration) to match InternalAuth's behavior.
func PlatformAuth() func(http.Handler) http.Handler {
	return platformAuth(lambdaenv.IsSet())
}

// platformAuth is the test seam; inLambda is injected so tests don't
// mutate process env captured at package init.
//
// Auth signal priority: MS_PLATFORM_TOKEN > MS_INTERNAL_SECRET.
// When MS_PLATFORM_TOKEN is set, the caller must present
// X-MS-Platform-Token. Otherwise, falls back to X-MS-Internal-Secret.
//
// Behavior matrix (secret = first non-empty of MS_PLATFORM_TOKEN, MS_INTERNAL_SECRET):
//
//	inLambda + secret unset → 503 (operator misconfig; platform alerting)
//	inLambda + secret set   → enforce: validate secret + identity headers
//	                          (Lambda authorizer path also still works —
//	                          if Identity is preset in ctx, that wins)
//	local    + secret unset → BYPASS: inject synthetic admin identity
//	                          (parallel to InternalAuth's local bypass)
//	local    + secret set   → enforce (e.g. `mirrorstack dev --tunnel`
//	                          where the CLI sets the secret)
func platformAuth(inLambda bool) func(http.Handler) http.Handler {
	readSecret := platformSecretReader()
	if expected, _ := readSecret(); expected == "" && !inLambda {
		log.Printf("mirrorstack: no platform secret set; platform routes bypass auth with synthetic admin identity (local dev)")
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Step 1: honor identity already attached upstream.
			if existing := Get(r.Context()); existing != nil && existing.AppRole != "" {
				next.ServeHTTP(w, r)
				return
			}

			expected, header := readSecret()

			// Step 2: local-dev bypass — inject synthetic admin.
			if expected == "" && !inLambda {
				ctx := Set(r.Context(), Identity{
					UserID:  "local-dev-user",
					AppID:   "local-dev-app",
					AppRole: RoleAdmin,
				})
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// Step 3: Lambda + no secret is a misconfig.
			if expected == "" {
				log.Printf("mirrorstack: platform auth rejected (no secret configured) from %s %s", r.RemoteAddr, r.URL.Path)
				httputil.JSON(w, http.StatusServiceUnavailable, httputil.ErrorResponse{
					Error: "service unavailable",
				})
				return
			}

			// Step 4: trusted-forwarder path. Validate secret, then read
			// identity headers.
			secret := r.Header.Get(header)
			if !constantTimeEqual(secret, expected) {
				log.Printf("mirrorstack: platform auth rejected (secret mismatch, header_present=%v) from %s %s", secret != "", r.RemoteAddr, r.URL.Path)
				httputil.JSON(w, http.StatusUnauthorized, httputil.ErrorResponse{Error: "authentication required"})
				return
			}

			userID := r.Header.Get(HeaderUserID)
			appID := r.Header.Get(HeaderAppID)
			appRole := r.Header.Get(HeaderAppRole)
			if userID == "" || appID == "" || appRole == "" {
				httputil.JSON(w, http.StatusUnauthorized, httputil.ErrorResponse{Error: "platform identity headers required"})
				return
			}

			ctx := Set(r.Context(), Identity{
				UserID:  userID,
				AppID:   appID,
				AppRole: appRole,
			})
			next.ServeHTTP(w, r.WithContext(ctx))
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
// Auth signal priority: MS_PLATFORM_TOKEN > MS_INTERNAL_SECRET.
// When MS_PLATFORM_TOKEN is set, the caller must present
// X-MS-Platform-Token. Otherwise, falls back to X-MS-Internal-Secret.
//
// Behavior matrix (secret = first non-empty of MS_PLATFORM_TOKEN, MS_INTERNAL_SECRET):
//
//	inLambda + secret unset → 503 (operator misconfig; platform alerting)
//	inLambda + secret set   → enforce matching header
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
	readSecret := platformSecretReader()
	if expected, _ := readSecret(); expected == "" {
		if inLambda {
			log.Printf("mirrorstack: WARNING — no platform secret set, all internal routes will be rejected")
		} else {
			log.Printf("mirrorstack: no platform secret set; internal routes bypass auth (local dev)")
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			expected, header := readSecret()

			if expected == "" {
				if inLambda {
					log.Printf("mirrorstack: internal auth rejected (no secret configured) from %s %s", r.RemoteAddr, r.URL.Path)
					httputil.JSON(w, http.StatusServiceUnavailable, httputil.ErrorResponse{
						Error: "service unavailable",
					})
					return
				}
				next.ServeHTTP(w, r)
				return
			}

			secret := r.Header.Get(header)
			if !constantTimeEqual(secret, expected) {
				log.Printf("mirrorstack: internal auth rejected (secret mismatch, header_present=%v) from %s %s", secret != "", r.RemoteAddr, r.URL.Path)
				httputil.JSON(w, http.StatusUnauthorized, httputil.ErrorResponse{Error: "internal authentication required"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CodeNotProxied is the stable error code returned when a request reaches a
// module's public/platform surface without the server-injected platform token
// — i.e. it did not come through the platform proxy (dispatch). The dispatch
// track matches on this code; do not rename it without updating that contract.
const CodeNotProxied = "not_proxied"

// RequireProxy returns middleware that rejects requests which did not arrive
// through the platform proxy. It guards the module's PUBLIC and PLATFORM
// request surfaces: a direct caller can spoof X-MS-App-ID / X-MS-User-ID /
// X-MS-App-Role, but NOT the X-MS-Platform-Token (a per-session secret the
// platform injects at the tunnel boundary; the browser never sees or sends
// it). Validating the token here, at the SDK trust boundary, makes every
// 3rd-party module inherit the protection for free.
//
// It mirrors InternalAuth's env gating exactly via platformSecretReader, so a
// module's standalone `go test` (no token configured) is unaffected, while the
// `mirrorstack dev` / tunnel / production HTTP paths (dispatch injects the
// token) enforce.
func RequireProxy() func(http.Handler) http.Handler {
	return requireProxy(lambdaenv.IsSet())
}

// requireProxy is the test seam; inLambda is injected so tests don't mutate
// process env captured at package init.
//
// Behavior matrix (secret = first non-empty of MS_PLATFORM_TOKEN[_FILE],
// MS_INTERNAL_SECRET; header = the corresponding X-MS-* header):
//
//	inLambda                → PASS through. The Lambda runtime strips all
//	                          X-MS-* headers and injects trusted identity from
//	                          the typed invoke payload (see runtime.NewLambdaHandler
//	                          + InjectResources), so there is no spoofable header
//	                          to guard and no token header to match. The payload
//	                          IS the trust boundary.
//	local + secret unset    → PASS through (pure standalone `go test`: the guard
//	                          must be inert so module unit tests still run).
//	local + secret set      → ENFORCE: require the matching token header. Absent
//	                          or mismatched → 403 not_proxied. This is the
//	                          `mirrorstack dev` / `--tunnel` / self-hosted HTTP
//	                          path where dispatch forwards raw headers and injects
//	                          X-MS-Platform-Token.
//
// CRITICAL: an accidentally-inert guard in production is a security hole; an
// accidentally-active guard in standalone tests bricks every module's tests.
// The gating above is deliberately identical to internalAuth's so the two
// trust signals stay in lockstep.
func requireProxy(inLambda bool) func(http.Handler) http.Handler {
	readSecret := platformSecretReader()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Lambda: headers already stripped + identity injected from payload.
			if inLambda {
				next.ServeHTTP(w, r)
				return
			}

			expected, header := readSecret()

			// No token configured: inert (standalone unit tests).
			if expected == "" {
				next.ServeHTTP(w, r)
				return
			}

			token := r.Header.Get(header)
			if !constantTimeEqual(token, expected) {
				log.Printf("mirrorstack: proxy guard rejected (token mismatch, header_present=%v) from %s %s", token != "", r.RemoteAddr, r.URL.Path)
				httputil.JSON(w, http.StatusForbidden, httputil.ErrorResponse{
					Error: "request did not come through the platform proxy",
					Code:  CodeNotProxied,
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// secretReader returns the current secret and which header carries it.
// The file-backed variant re-reads on every call so a refreshed token
// (CLI reconnect) is picked up without restarting the module process.
type secretReader func() (expected, header string)

// platformSecretReader builds the reader once at middleware construction.
//   - MS_PLATFORM_TOKEN_FILE set → read from file per call (dev)
//   - MS_PLATFORM_TOKEN set      → static, captured once (prod / tunnel)
//   - MS_INTERNAL_SECRET set     → static, captured once (legacy)
func platformSecretReader() secretReader {
	if file := os.Getenv("MS_PLATFORM_TOKEN_FILE"); file != "" {
		return func() (string, string) {
			data, err := os.ReadFile(file)
			if err != nil {
				return "", HeaderPlatformToken
			}
			return strings.TrimSpace(string(data)), HeaderPlatformToken
		}
	}
	if v := os.Getenv("MS_PLATFORM_TOKEN"); v != "" {
		return func() (string, string) { return v, HeaderPlatformToken }
	}
	v := os.Getenv("MS_INTERNAL_SECRET")
	return func() (string, string) { return v, HeaderInternalSecret }
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
