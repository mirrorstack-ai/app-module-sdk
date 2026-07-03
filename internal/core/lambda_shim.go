package core

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/internal/httputil"
	"github.com/mirrorstack-ai/app-module-sdk/internal/runtime"
)

// lambdaInvokePath is the reserved dev route dispatch's localHTTPInvoker POSTs
// LambdaRequest envelopes to (via MS_MODULE_LAMBDA_DEV_URL). Mounted only
// outside Lambda mode — see mountSystemRoutes.
const lambdaInvokePath = "/__mirrorstack/lambda-invoke"

// lambdaInvokeSecretEnvVar is the module-side copy of dispatch's
// MS_MODULE_LAMBDA_INTERNAL_SECRET — dispatch injects that value into every
// envelope's headers map as X-MS-Internal-Secret. A DEDICATED var, not the
// MS_PLATFORM_TOKEN[_FILE] > MS_INTERNAL_SECRET chain: the dev runner points
// that chain at the per-session tunnel token (and the log-shipper secret),
// neither of which dispatch puts inside lambda envelopes.
const lambdaInvokeSecretEnvVar = "MS_LAMBDA_INTERNAL_SECRET"

// lambdaInvokeShim returns the POST /__mirrorstack/lambda-invoke handler: the
// dev-mode stand-in for the real Lambda transport. It feeds the raw envelope
// through the same runtime.NewLambdaHandler closure production uses, which
// injects the envelope's typed identity into ctx BEFORE any router middleware
// runs — so the shim MUST gate itself: on the dev HTTP path PlatformAuth
// passes preset identity through with no secret check, and an ungated shim
// would let any local caller forge identity.
//
// Gate matrix (envelope secret = headers["X-MS-Internal-Secret"], compared
// against MS_LAMBDA_INTERNAL_SECRET; mirrors internalAuth's matrix):
//
//	lambda secret set                → enforce: constant-time compare,
//	                                   401 on absent/mismatch
//	unset + platform chain set       → 503 fail closed: an enforcing config
//	                                   (tunnel/self-hosted) with no usable
//	                                   lambda secret must reject, never bypass
//	nothing configured               → bypass: plain `mirrorstack dev`, where
//	                                   every other guard on this surface is
//	                                   already inert/synthetic
//
// Rejections use OUTER HTTP statuses (400/401/503): dispatch's transport
// treats any >=300 as a generic module-unavailable fault, so no identity or
// gate detail leaks to the client. A delivered invoke always writes HTTP 200
// — the module's real status rides inside the LambdaResponse envelope.
func (m *Module) lambdaInvokeShim() http.HandlerFunc {
	// Captured once at mount, matching internalAuth's construction-time
	// capture contract (env vars don't change at runtime).
	expected := os.Getenv(lambdaInvokeSecretEnvVar)
	platformConfigured := auth.SecretConfigured()
	invoke := runtime.NewLambdaHandler(m.router)

	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, internalRouteBodyCap))
		if err != nil {
			httputil.JSON(w, http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid request body"})
			return
		}

		// Peek ONLY the gate-relevant fields before anything touches the
		// envelope's identity: the secret, and the inner path (an envelope
		// addressed back at the shim would re-enter it with an
		// attacker-controlled inner body).
		var env struct {
			Path    string            `json:"path"`
			Headers map[string]string `json:"headers"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			httputil.JSON(w, http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid lambda invoke envelope"})
			return
		}
		if isLambdaInvokePath(env.Path) {
			httputil.JSON(w, http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid lambda invoke envelope"})
			return
		}

		switch {
		case expected != "":
			// Dispatch writes the literal key "X-MS-Internal-Secret"; iterate
			// with EqualFold so a differently-cased producer still gates
			// correctly instead of silently 401ing every invoke.
			var got string
			for k, v := range env.Headers {
				if strings.EqualFold(k, auth.HeaderInternalSecret) {
					got = v
					break
				}
			}
			if subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
				m.logger.Printf("lambda-invoke shim rejected (secret mismatch, present=%v) from %s", got != "", r.RemoteAddr)
				httputil.JSON(w, http.StatusUnauthorized, httputil.ErrorResponse{Error: "internal authentication required"})
				return
			}
		case platformConfigured:
			m.logger.Printf("lambda-invoke shim rejected (%s not set while a platform secret is configured) from %s", lambdaInvokeSecretEnvVar, r.RemoteAddr)
			httputil.JSON(w, http.StatusServiceUnavailable, httputil.ErrorResponse{Error: "service unavailable"})
			return
		}

		// The outer request's context carries chi's (already-consumed)
		// RouteContext for this same mux — the synthetic inner request must
		// start routing fresh, or chi reuses that state and 404s every path.
		ctx := context.WithValue(r.Context(), chi.RouteCtxKey, nil)

		// NewLambdaHandler never returns a non-nil error — malformed payloads
		// and handler failures come back INSIDE the envelope (statusCode
		// field), which is what dispatch decodes out of a 2xx body.
		resp, _ := invoke(ctx, json.RawMessage(body))
		httputil.JSON(w, http.StatusOK, resp)
	}
}

// isLambdaInvokePath reports whether an envelope's inner path addresses the
// shim itself, normalized the way NewLambdaHandler builds the synthetic URL
// (leading slash added; query/fragment not part of the routed path).
func isLambdaInvokePath(path string) bool {
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path == lambdaInvokePath
}
