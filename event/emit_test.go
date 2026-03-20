package event_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/mirrorstack-ai/app-module-sdk/event"
	"github.com/mirrorstack-ai/app-module-sdk/handler"
)

// --- Emit ---

func TestEmit_AsyncInvoke(t *testing.T) {
	mock := &mockLambda{}
	client := event.NewClient(mock, "arn:aws:lambda:us-east-1:123:function:platform", "video")
	ctx := contextWithHeaders(t, map[string]string{
		handler.HeaderAppID:      "app-456",
		handler.HeaderSchemaName: "app_x7k2",
		handler.HeaderRequestID:  "req-001",
	})

	err := client.Emit(ctx, "transcode_completed", map[string]any{"videoId": "v-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mock.gotInput == nil {
		t.Fatal("Invoke was not called")
	}
	if *mock.gotInput.FunctionName != "arn:aws:lambda:us-east-1:123:function:platform" {
		t.Errorf("ARN: got %q", *mock.gotInput.FunctionName)
	}
	if mock.gotInput.InvocationType != lambdatypes.InvocationTypeEvent {
		t.Errorf("invocation type: got %q, want Event (async)", mock.gotInput.InvocationType)
	}

	var req map[string]any
	if err := json.Unmarshal(mock.gotInput.Payload, &req); err != nil {
		t.Fatalf("unmarshal invoke payload: %v", err)
	}

	if req["source"] != "internal" {
		t.Errorf("source: got %v, want %q", req["source"], "internal")
	}
	if req["action"] != "emit" {
		t.Errorf("action: got %v, want %q", req["action"], "emit")
	}
	if req["module_id"] != "video" {
		t.Errorf("module_id: got %v, want %q", req["module_id"], "video")
	}
	if req["app_id"] != "app-456" {
		t.Errorf("app_id: got %v, want %q", req["app_id"], "app-456")
	}
	if req["request_id"] != "req-001" {
		t.Errorf("request_id: got %v, want %q", req["request_id"], "req-001")
	}

	evt, ok := req["event"].(map[string]any)
	if !ok {
		t.Fatal("event field missing or wrong type")
	}
	if evt["type"] != "video.transcode_completed" {
		t.Errorf("event type: got %v, want %q", evt["type"], "video.transcode_completed")
	}
}

func TestEmit_InvokeError_ReturnsError(t *testing.T) {
	mock := &mockLambda{err: fmt.Errorf("connection refused")}
	client := event.NewClient(mock, "arn:platform", "video")
	ctx := contextWithHeaders(t, map[string]string{handler.HeaderAppID: "app-1", handler.HeaderSchemaName: "app_test"})

	err := client.Emit(ctx, "something", nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestEmit_PayloadTooLarge_ReturnsError(t *testing.T) {
	mock := &mockLambda{}
	client := event.NewClient(mock, "arn:platform", "video")
	ctx := contextWithHeaders(t, map[string]string{handler.HeaderAppID: "app-1", handler.HeaderSchemaName: "app_test"})

	bigPayload := map[string]string{"data": strings.Repeat("x", 250*1024)}
	err := client.Emit(ctx, "big_event", bigPayload)
	if err == nil {
		t.Fatal("expected error for oversized payload")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error should mention size limit: got %q", err.Error())
	}
	if mock.gotInput != nil {
		t.Error("Lambda should not have been invoked for oversized payload")
	}
}

func TestEmit_BareContext_ReturnsError(t *testing.T) {
	mock := &mockLambda{}
	client := event.NewClient(mock, "arn:platform", "video")

	err := client.Emit(context.Background(), "something", nil)
	if err == nil {
		t.Fatal("expected error for bare context")
	}
	if !strings.Contains(err.Error(), "missing app_id") {
		t.Errorf("error should mention missing app_id: got %q", err.Error())
	}
}

// --- Call ---

func TestCall_SyncInvoke(t *testing.T) {
	respPayload, _ := json.Marshal(map[string]any{"success": true, "newBalance": 80})
	mock := &mockLambda{
		output: &lambda.InvokeOutput{Payload: respPayload},
	}

	client := event.NewClient(mock, "arn:platform", "video")
	ctx := contextWithHeaders(t, map[string]string{
		handler.HeaderAppID:      "app-789",
		handler.HeaderSchemaName: "app_z1",
		handler.HeaderRequestID:  "req-002",
	})

	var resp struct {
		Success    bool `json:"success"`
		NewBalance int  `json:"newBalance"`
	}
	err := client.Call(ctx, "credit", "v1", "/deduct", map[string]any{
		"userId": "u-1",
		"amount": 20,
	}, &resp)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.gotInput.InvocationType != lambdatypes.InvocationTypeRequestResponse {
		t.Errorf("invocation type: got %q, want RequestResponse (sync)", mock.gotInput.InvocationType)
	}

	var req map[string]any
	if err := json.Unmarshal(mock.gotInput.Payload, &req); err != nil {
		t.Fatalf("unmarshal invoke payload: %v", err)
	}

	if req["action"] != "call" {
		t.Errorf("action: got %v, want %q", req["action"], "call")
	}
	if req["target"] != "credit" {
		t.Errorf("target: got %v, want %q", req["target"], "credit")
	}
	if req["version"] != "v1" {
		t.Errorf("version: got %v, want %q", req["version"], "v1")
	}
	if req["path"] != "/deduct" {
		t.Errorf("path: got %v, want %q", req["path"], "/deduct")
	}
	if req["request_id"] != "req-002" {
		t.Errorf("request_id: got %v, want %q", req["request_id"], "req-002")
	}
	if !resp.Success {
		t.Error("expected success=true")
	}
	if resp.NewBalance != 80 {
		t.Errorf("newBalance: got %d, want 80", resp.NewBalance)
	}
}

func TestCall_FunctionError_ReturnsCallError(t *testing.T) {
	funcErr := "Handled"
	errorBody := `{"error":{"code":"insufficient_credits","message":"balance too low"}}`
	mock := &mockLambda{
		output: &lambda.InvokeOutput{
			StatusCode:    402,
			FunctionError: &funcErr,
			Payload:       []byte(errorBody),
		},
	}
	client := event.NewClient(mock, "arn:platform", "video")
	ctx := contextWithHeaders(t, map[string]string{handler.HeaderAppID: "app-1", handler.HeaderSchemaName: "app_test"})

	var resp struct{}
	err := client.Call(ctx, "credit", "v1", "/deduct", nil, &resp)
	if err == nil {
		t.Fatal("expected error for function error")
	}

	var callErr *event.CallError
	if !errors.As(err, &callErr) {
		t.Fatalf("expected *CallError, got %T: %v", err, err)
	}
	if callErr.StatusCode != 402 {
		t.Errorf("status: got %d, want 402", callErr.StatusCode)
	}
	if callErr.FuncError != "Handled" {
		t.Errorf("funcError: got %q, want %q", callErr.FuncError, "Handled")
	}
	if !strings.Contains(callErr.Body, "insufficient_credits") {
		t.Errorf("body should contain error detail: got %q", callErr.Body)
	}
}

func TestCall_InvokeError_ReturnsError(t *testing.T) {
	mock := &mockLambda{err: fmt.Errorf("throttled")}
	client := event.NewClient(mock, "arn:platform", "video")
	ctx := contextWithHeaders(t, map[string]string{handler.HeaderAppID: "app-1", handler.HeaderSchemaName: "app_test"})

	err := client.Call(ctx, "credit", "v1", "/deduct", nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCall_NilResult(t *testing.T) {
	mock := &mockLambda{
		output: &lambda.InvokeOutput{},
	}
	client := event.NewClient(mock, "arn:platform", "video")
	ctx := contextWithHeaders(t, map[string]string{handler.HeaderAppID: "app-1", handler.HeaderSchemaName: "app_test"})

	err := client.Call(ctx, "credit", "v1", "/ping", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCall_MalformedResponse_ReturnsError(t *testing.T) {
	mock := &mockLambda{
		output: &lambda.InvokeOutput{Payload: []byte("not json")},
	}
	client := event.NewClient(mock, "arn:platform", "video")
	ctx := contextWithHeaders(t, map[string]string{handler.HeaderAppID: "app-1", handler.HeaderSchemaName: "app_test"})

	var resp struct{ Name string }
	err := client.Call(ctx, "credit", "v1", "/test", nil, &resp)
	if err == nil {
		t.Fatal("expected error for malformed response")
	}
	if !strings.Contains(err.Error(), "decode response") {
		t.Errorf("error should mention decode: got %q", err.Error())
	}
}

func TestCall_PayloadTooLarge_ReturnsError(t *testing.T) {
	mock := &mockLambda{}
	client := event.NewClient(mock, "arn:platform", "video")
	ctx := contextWithHeaders(t, map[string]string{handler.HeaderAppID: "app-1", handler.HeaderSchemaName: "app_test"})

	bigBody := map[string]string{"data": strings.Repeat("x", 6*1024*1024)}
	err := client.Call(ctx, "credit", "v1", "/big", bigBody, nil)
	if err == nil {
		t.Fatal("expected error for oversized payload")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error should mention size limit: got %q", err.Error())
	}
}

func TestCall_BareContext_ReturnsError(t *testing.T) {
	mock := &mockLambda{}
	client := event.NewClient(mock, "arn:platform", "video")

	err := client.Call(context.Background(), "credit", "v1", "/test", nil, nil)
	if err == nil {
		t.Fatal("expected error for bare context")
	}
}

// --- RunTask ---

func TestRunTask_Success(t *testing.T) {
	respPayload, _ := json.Marshal(map[string]any{"task_id": "task-abc", "status": "pending"})
	mock := &mockLambda{
		output: &lambda.InvokeOutput{Payload: respPayload},
	}
	client := event.NewClient(mock, "arn:platform", "video")
	ctx := contextWithHeaders(t, map[string]string{
		handler.HeaderAppID:      "app-1",
		handler.HeaderSchemaName: "app_x7k2",
	})

	result, err := client.RunTask(ctx, "transcode", map[string]any{
		"videoId": "v-1",
		"quality": []string{"720p", "1080p"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TaskID != "task-abc" {
		t.Errorf("taskID: got %q, want %q", result.TaskID, "task-abc")
	}
	if result.Status != "pending" {
		t.Errorf("status: got %q, want %q", result.Status, "pending")
	}

	// Verify it calls platform with correct payload.
	var req map[string]any
	if err := json.Unmarshal(mock.gotInput.Payload, &req); err != nil {
		t.Fatalf("unmarshal invoke payload: %v", err)
	}
	if req["target"] != "platform" {
		t.Errorf("target: got %v, want %q", req["target"], "platform")
	}
	if req["path"] != "/tasks/run" {
		t.Errorf("path: got %v, want %q", req["path"], "/tasks/run")
	}

	// Verify body contains module_id and task_type.
	bodyRaw, _ := json.Marshal(req["body"])
	var body map[string]any
	if err := json.Unmarshal(bodyRaw, &body); err != nil {
		// body is stored as string in the map, try parsing req["body"] directly
		bodyStr, ok := req["body"].(string)
		if ok {
			json.Unmarshal([]byte(bodyStr), &body)
		}
	}
	if body != nil {
		if body["module_id"] != "video" {
			t.Errorf("body.module_id: got %v, want %q", body["module_id"], "video")
		}
		if body["task_type"] != "transcode" {
			t.Errorf("body.task_type: got %v, want %q", body["task_type"], "transcode")
		}
	}
}

func TestRunTask_PlatformError_ReturnsError(t *testing.T) {
	funcErr := "Handled"
	mock := &mockLambda{
		output: &lambda.InvokeOutput{
			StatusCode:    500,
			FunctionError: &funcErr,
			Payload:       []byte(`{"error":"task definition not found"}`),
		},
	}
	client := event.NewClient(mock, "arn:platform", "video")
	ctx := contextWithHeaders(t, map[string]string{handler.HeaderAppID: "app-1", handler.HeaderSchemaName: "app_test"})

	result, err := client.RunTask(ctx, "unknown_task", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if result != nil {
		t.Error("result should be nil on error")
	}
}

func TestRunTask_UsesSync(t *testing.T) {
	respPayload, _ := json.Marshal(map[string]any{"task_id": "t-1", "status": "pending"})
	mock := &mockLambda{
		output: &lambda.InvokeOutput{Payload: respPayload},
	}
	client := event.NewClient(mock, "arn:platform", "video")
	ctx := contextWithHeaders(t, map[string]string{handler.HeaderAppID: "app-1", handler.HeaderSchemaName: "app_test"})

	_, err := client.RunTask(ctx, "export", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// RunTask must be sync — we need the task ID back.
	if mock.gotInput.InvocationType != lambdatypes.InvocationTypeRequestResponse {
		t.Errorf("invocation type: got %q, want RequestResponse", mock.gotInput.InvocationType)
	}
}
