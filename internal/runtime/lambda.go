package runtime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/cache"
	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/httputil"
	"github.com/mirrorstack-ai/app-module-sdk/storage"
)

// AppSchemaPattern validates a per-app Postgres schema name (app_<uuid> with
// hyphens replaced by underscores). Exported so the dev runtime, which derives
// the schema from X-MS-App-ID instead of receiving it in the Lambda payload,
// validates it identically — keeping dev and prod from drifting on the shape.
var AppSchemaPattern = regexp.MustCompile(`^app_[a-z0-9_]+$`)

// Resources holds per-invocation credentials for all platform services.
type Resources struct {
	DB      *db.Credential      `json:"db,omitempty"`
	Cache   *cache.Credential   `json:"cache,omitempty"`
	Storage *storage.Credential `json:"storage,omitempty"`
}

// DependencyGrant is the deployed cross-module read manifest entry ridden down
// the envelope (decision 18 §3). Aliased to db.DependencyGrant so the ctx seam
// (db.WithDependencies) stores the canonical type without an import cycle
// (db must not import runtime); the wire tags live on db.DependencyGrant.
type DependencyGrant = db.DependencyGrant

// LambdaRequest is the payload format sent by the platform via Lambda Invoke SDK.
type LambdaRequest struct {
	Method    string            `json:"method"`
	Path      string            `json:"path"`
	Headers   map[string]string `json:"headers"`
	Body      string            `json:"body"`
	Resources *Resources        `json:"resources,omitempty"`
	// Dependencies is the platform-resolved cross-module read manifest — one
	// entry per installed declared producer, holding only exposed+consented
	// tables at the running version (decision 18 §3). Advisory routing only;
	// the install-time GRANT is the authorizer. omitempty keeps old-SDK
	// backward-compat and old-platform absence (nil → deployed reads stay
	// dev-plane-only, the rollout gate).
	Dependencies []DependencyGrant `json:"dependencies,omitempty"`
	// Trusted fields — injected by platform, not from user headers
	UserID    string `json:"userId,omitempty"`
	AppID     string `json:"appId,omitempty"`
	AppRole   string `json:"appRole,omitempty"`
	AppSchema string `json:"appSchema,omitempty"`
}

// LambdaResponse is returned to the platform after handling a request.
type LambdaResponse struct {
	StatusCode int                 `json:"statusCode"`
	Headers    map[string][]string `json:"headers"`
	Body       string              `json:"body"`
}

func jsonError(code int, msg string) LambdaResponse {
	b, _ := json.Marshal(httputil.ErrorResponse{Error: msg})
	return LambdaResponse{
		StatusCode: code,
		Headers:    map[string][]string{"Content-Type": {"application/json"}},
		Body:       string(b),
	}
}

// msAuthSecretHeaders are the platform-injected auth SECRET headers (lower-cased
// for comparison). Unlike the spoofable identity-claim headers (x-ms-user-id,
// x-ms-app-id, x-ms-app-role), these are credentials the platform sets and the
// module's internalAuth / RequireProxy middleware must still validate — so they
// survive the x-ms-* strip on the Lambda path, exactly as the dev tunnel injects
// them. Kept in sync with auth.HeaderInternalSecret / auth.HeaderPlatformToken
// (asserted in lambda_test.go). See decisions/09 §4 (prod module transport).
var msAuthSecretHeaders = map[string]bool{
	"x-ms-internal-secret": true,
	"x-ms-platform-token":  true,
}

// isStrippedIdentityHeader reports whether an inbound header must be dropped
// before the module router sees it: every x-ms-* header EXCEPT the platform-auth
// secrets in msAuthSecretHeaders. Trusted identity arrives via the typed
// LambdaRequest fields, so identity-claim headers are always stripped.
func isStrippedIdentityHeader(k string) bool {
	if len(k) < 5 || !strings.EqualFold(k[:5], "x-ms-") {
		return false
	}
	return !msAuthSecretHeaders[strings.ToLower(k)]
}

// NewLambdaHandler wraps an http.Handler into a function compatible with
// aws-lambda-go's lambda.Start().
func NewLambdaHandler(handler http.Handler) func(context.Context, json.RawMessage) (LambdaResponse, error) {
	return func(ctx context.Context, payload json.RawMessage) (LambdaResponse, error) {
		var req LambdaRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return jsonError(400, "invalid request payload"), nil
		}

		// Ensure path is relative to prevent host injection
		path := req.Path
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}

		var body io.Reader = http.NoBody
		if req.Body != "" {
			body = strings.NewReader(req.Body)
		}

		httpReq, err := http.NewRequestWithContext(ctx, req.Method, "http://localhost"+path, body)
		if err != nil {
			return jsonError(500, "failed to build request"), nil
		}

		// Copy headers, stripping spoofable X-MS-* identity CLAIMS (user/app/
		// role) — trusted identity arrives via the typed LambdaRequest fields
		// below, never from headers. The platform-auth SECRET headers are exempt
		// (see msAuthSecretHeaders): they are credentials the module's
		// internalAuth / RequireProxy middleware must still see. Safe because the
		// platform builds a fresh header set per invoke — nothing client-supplied
		// reaches here.
		for k, v := range req.Headers {
			if isStrippedIdentityHeader(k) {
				continue
			}
			httpReq.Header.Set(k, v)
		}

		// Inject trusted values from typed payload fields into context.
		// InjectResources is the shared injection function used by both
		// Lambda and task worker paths — see inject.go.
		reqCtx, err := InjectResources(httpReq.Context(), InjectParams{
			Resources:    req.Resources,
			Dependencies: req.Dependencies,
			UserID:       req.UserID,
			AppID:        req.AppID,
			AppRole:      req.AppRole,
			AppSchema:    req.AppSchema,
		})
		if err != nil {
			return jsonError(400, err.Error()), nil
		}
		// Payload-trust mark: RequireProxy passes a marked request through
		// exactly like Lambda mode (the envelope never carries the per-session
		// X-MS-Platform-Token). NewLambdaHandler must stay the ONLY writer —
		// its callers are the real Lambda transport and the dev lambda-invoke
		// shim, which gates on the envelope secret before invoking.
		reqCtx = auth.WithPayloadTrust(reqCtx)
		httpReq = httpReq.WithContext(reqCtx)

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httpReq)

		result := rec.Result()

		return LambdaResponse{
			StatusCode: result.StatusCode,
			Headers:    result.Header,
			Body:       rec.Body.String(),
		}, nil
	}
}
