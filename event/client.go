package event

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/mirrorstack-ai/app-module-sdk/handler"
)

const (
	// maxEmitPayload is the safe limit for async Lambda invoke (256KB hard limit).
	maxEmitPayload = 200 * 1024
	// maxCallPayload is the safe limit for sync Lambda invoke (6MB hard limit).
	maxCallPayload = 5 * 1024 * 1024
	// maxResponseBody caps how much data httpInvoker reads from local dev platform.
	maxResponseBody = 6 * 1024 * 1024
)

// Event is the standard payload delivered to subscriber handlers.
type Event struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	AppID     string          `json:"app_id"`
	Source    string          `json:"source"`
	Payload   json.RawMessage `json:"payload"`
	Timestamp time.Time       `json:"timestamp"`
}

// HandlerFunc processes an incoming event.
type HandlerFunc func(w http.ResponseWriter, r *http.Request, evt Event)

// CallError is returned when a sync Call to another module fails with a
// structured error response.
type CallError struct {
	StatusCode int
	Body       string
	FuncError  string
}

func (e *CallError) Error() string {
	return fmt.Sprintf("call error (status %d, %s): %s", e.StatusCode, e.FuncError, e.Body)
}

// LambdaInvoker is the subset of the Lambda client we need.
// Allows mocking in tests.
type LambdaInvoker interface {
	Invoke(ctx context.Context, params *lambda.InvokeInput, optFns ...func(*lambda.Options)) (*lambda.InvokeOutput, error)
}

// Client handles outbound inter-module communication via Lambda invoke.
type Client struct {
	lambda         LambdaInvoker
	platformTarget string
	moduleID       string
}

// NewClient creates an event client using Lambda invoke (production).
//
//	cfg, _ := config.LoadDefaultConfig(ctx)
//	lambdaClient := lambda.NewFromConfig(cfg)
//	client := event.NewClient(lambdaClient, os.Getenv("PLATFORM_ARN"), "video")
func NewClient(lambdaClient LambdaInvoker, platformARN, moduleID string) *Client {
	return &Client{
		lambda:         lambdaClient,
		platformTarget: platformARN,
		moduleID:       moduleID,
	}
}

// NewHTTPClient creates an event client using HTTP POST (local dev).
// Same interface as NewClient — module code doesn't change.
//
//	client := event.NewHTTPClient("http://localhost:3000", "dev-secret", "video")
func NewHTTPClient(platformURL, secret, moduleID string) *Client {
	return &Client{
		lambda: &httpInvoker{
			platformURL: platformURL,
			secret:      secret,
			httpClient:  &http.Client{Timeout: 30 * time.Second},
		},
		platformTarget: platformURL,
		moduleID:       moduleID,
	}
}

// httpInvoker implements LambdaInvoker by translating to HTTP POST.
// Used for local development where Lambda invoke is unavailable.
type httpInvoker struct {
	platformURL string
	secret      string
	httpClient  *http.Client
}

func (h *httpInvoker) Invoke(ctx context.Context, params *lambda.InvokeInput, _ ...func(*lambda.Options)) (*lambda.InvokeOutput, error) {
	// Async invoke: fire-and-forget to simulate Lambda Event invocation.
	if params.InvocationType == lambdatypes.InvocationTypeEvent {
		go func() {
			asyncCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(asyncCtx, http.MethodPost, h.platformURL+"/internal/invoke", bytes.NewReader(params.Payload))
			if err != nil {
				log.Printf("event: async emit request creation failed: %v", err)
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(handler.HeaderInternalSecret, h.secret)
			resp, err := h.httpClient.Do(req)
			if err != nil {
				log.Printf("event: async emit delivery failed: %v", err)
				return
			}
			resp.Body.Close()
		}()
		return &lambda.InvokeOutput{StatusCode: 202}, nil
	}

	// Sync invoke: blocking call, return response.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.platformURL+"/internal/invoke", bytes.NewReader(params.Payload))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(handler.HeaderInternalSecret, h.secret)

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	out := &lambda.InvokeOutput{
		StatusCode: int32(resp.StatusCode),
		Payload:    body,
	}

	if resp.StatusCode >= 400 {
		funcErr := string(body)
		out.FunctionError = &funcErr
	}

	return out, nil
}
