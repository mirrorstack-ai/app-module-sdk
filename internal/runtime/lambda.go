package runtime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
)

// LambdaRequest is the payload format sent by the platform via Lambda Invoke SDK.
type LambdaRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// LambdaResponse is returned to the platform after handling a request.
type LambdaResponse struct {
	StatusCode int                 `json:"statusCode"`
	Headers    map[string][]string `json:"headers"`
	Body       string              `json:"body"`
}

type errorBody struct {
	Error string `json:"error"`
}

func jsonError(code int, msg string) LambdaResponse {
	b, _ := json.Marshal(errorBody{Error: msg})
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
		for k, v := range req.Headers {
			httpReq.Header.Set(k, v)
		}

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
