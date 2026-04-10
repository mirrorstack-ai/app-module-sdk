package runtime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"

	"github.com/mirrorstack-ai/app-module-sdk/cache"
	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/httputil"
	"github.com/mirrorstack-ai/app-module-sdk/storage"
)

var schemaPattern = regexp.MustCompile(`^app_[a-z0-9_]+$`)

// Resources holds per-invocation credentials for all platform services.
type Resources struct {
	DB      *db.Credential      `json:"db,omitempty"`
	Cache   *cache.Credential   `json:"cache,omitempty"`
	Storage *storage.Credential `json:"storage,omitempty"`
}

// LambdaRequest is the payload format sent by the platform via Lambda Invoke SDK.
type LambdaRequest struct {
	Method    string            `json:"method"`
	Path      string            `json:"path"`
	Headers   map[string]string `json:"headers"`
	Body      string            `json:"body"`
	Resources *Resources        `json:"resources,omitempty"`
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

		// Copy headers but strip X-MS-* to prevent spoofing
		for k, v := range req.Headers {
			if len(k) >= 5 && strings.EqualFold(k[:5], "x-ms-") {
				continue
			}
			httpReq.Header.Set(k, v)
		}

		// Inject trusted values from typed payload fields into context.
		// InjectResources is the shared injection function used by both
		// Lambda and task worker paths — see inject.go.
		reqCtx, err := InjectResources(httpReq.Context(), InjectParams{
			Resources: req.Resources,
			UserID:    req.UserID,
			AppID:     req.AppID,
			AppRole:   req.AppRole,
			AppSchema: req.AppSchema,
		})
		if err != nil {
			return jsonError(400, err.Error()), nil
		}
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
