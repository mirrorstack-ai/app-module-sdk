package event

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/mirrorstack-ai/app-module-sdk/handler"
)

// internalRequest is the payload sent via Lambda invoke to the platform.
type internalRequest struct {
	Source     string `json:"source"`
	Action     string `json:"action"`
	ModuleID   string `json:"module_id"`
	AppID      string `json:"app_id"`
	SchemaName string `json:"schema_name"`
	RequestID  string `json:"request_id,omitempty"`

	// For emit
	Event *emitPayload `json:"event,omitempty"`

	// For call
	Target  string          `json:"target,omitempty"`
	Version string          `json:"version,omitempty"`
	Path    string          `json:"path,omitempty"`
	Body    json.RawMessage `json:"body,omitempty"`
}

type emitPayload struct {
	Type      string    `json:"type"`
	Payload   any       `json:"payload"`
	Timestamp time.Time `json:"timestamp"`
}

// Emit publishes an event to the platform for delivery to subscribers.
// Uses async Lambda invoke (fire-and-forget).
//
// A nil error means the event was accepted for async delivery, not that
// it was successfully delivered to all subscribers.
//
//	err := client.Emit(ctx, "transcode_completed", map[string]any{"videoId": id})
func (c *Client) Emit(ctx context.Context, eventType string, payload any) error {
	if err := requireContext(ctx); err != nil {
		return err
	}

	req := internalRequest{
		Source:     "internal",
		Action:     "emit",
		ModuleID:   c.moduleID,
		AppID:      handler.GetAppID(ctx),
		SchemaName: handler.GetSchemaName(ctx),
		RequestID:  handler.GetRequestID(ctx),
		Event: &emitPayload{
			Type:      c.moduleID + "." + eventType,
			Payload:   payload,
			Timestamp: time.Now().UTC(),
		},
	}
	return c.invoke(ctx, req, lambdatypes.InvocationTypeEvent, nil)
}

// Call makes a synchronous call to another module via the platform Lambda.
// Use this when you need a response (e.g., credit deduction).
// If result is nil, the response body is discarded.
//
// Returns *CallError when the target module returns a structured error,
// allowing callers to inspect status and details.
//
//	var resp DeductResponse
//	err := client.Call(ctx, "credit", "v1", "/deduct", reqBody, &resp)
func (c *Client) Call(ctx context.Context, targetModule, version, path string, reqBody any, result any) error {
	if err := requireContext(ctx); err != nil {
		return err
	}

	var body json.RawMessage
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		body = b
	}

	req := internalRequest{
		Source:     "internal",
		Action:     "call",
		ModuleID:   c.moduleID,
		AppID:      handler.GetAppID(ctx),
		SchemaName: handler.GetSchemaName(ctx),
		RequestID:  handler.GetRequestID(ctx),
		Target:     targetModule,
		Version:    version,
		Path:       path,
		Body:       body,
	}
	return c.invoke(ctx, req, lambdatypes.InvocationTypeRequestResponse, result)
}

func (c *Client) invoke(ctx context.Context, req internalRequest, invocationType lambdatypes.InvocationType, result any) error {
	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	// Validate payload size against Lambda limits.
	limit := maxCallPayload
	if invocationType == lambdatypes.InvocationTypeEvent {
		limit = maxEmitPayload
	}
	if len(payload) > limit {
		return fmt.Errorf("payload size %d bytes exceeds %d byte limit for %s invocation", len(payload), limit, invocationType)
	}

	out, err := c.lambda.Invoke(ctx, &lambda.InvokeInput{
		FunctionName:   &c.platformTarget,
		InvocationType: invocationType,
		Payload:        payload,
	})
	if err != nil {
		return fmt.Errorf("lambda invoke: %w", err)
	}

	// FunctionError is only populated for RequestResponse (sync) invocations.
	// Async invocations return 202 immediately; errors go to the platform DLQ.
	if invocationType == lambdatypes.InvocationTypeRequestResponse && out.FunctionError != nil {
		return &CallError{
			StatusCode: int(out.StatusCode),
			FuncError:  *out.FunctionError,
			Body:       string(out.Payload),
		}
	}

	if result != nil && len(out.Payload) > 0 {
		if err := json.Unmarshal(out.Payload, result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}

	return nil
}

// TaskResult is the response from RunTask.
type TaskResult struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"`
}

// RunTask starts a long-running ECS Fargate task via the platform.
// The platform handles cluster, subnet, task definition — the module
// only provides the task type and payload.
//
// Use this for work that exceeds Lambda's 15-minute timeout
// (video transcoding, batch AI, data export).
//
//	result, err := client.RunTask(ctx, "transcode", map[string]any{
//	    "videoId": videoID,
//	    "quality": []string{"720p", "1080p"},
//	})
//	// result.TaskID for polling: GET /tasks/{taskId}
func (c *Client) RunTask(ctx context.Context, taskType string, payload any) (*TaskResult, error) {
	var result TaskResult
	err := c.Call(ctx, "platform", "v1", "/tasks/run", map[string]any{
		"module_id": c.moduleID,
		"task_type": taskType,
		"payload":   payload,
	}, &result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// requireContext validates that the context has been enriched by ExtractContext.
func requireContext(ctx context.Context) error {
	if handler.GetAppID(ctx) == "" {
		return errors.New("event: context missing app_id — ensure handler.ExtractContext middleware is applied")
	}
	if handler.GetSchemaName(ctx) == "" {
		return errors.New("event: context missing schema_name — ensure handler.ExtractContext middleware is applied")
	}
	return nil
}
