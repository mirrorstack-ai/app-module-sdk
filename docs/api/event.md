---
title: event Package API Reference
description: Emit, Call, RunTask, Register, and event handlers
order: 2
---

## Client Creation

### NewClient (Lambda)

```go
func NewClient(lambdaClient LambdaInvoker, platformARN, moduleID string) *Client
```

Create event client using AWS Lambda invoke (production).

```go
cfg, _ := awsconfig.LoadDefaultConfig(ctx)
lambdaClient := awslambda.NewFromConfig(cfg)
ec := event.NewClient(lambdaClient, os.Getenv("PLATFORM_ARN"), "video")
```

### NewHTTPClient

```go
func NewHTTPClient(platformURL, secret, moduleID string) *Client
```

Create event client using HTTP POST (local development).

```go
ec := event.NewHTTPClient("http://localhost:3000", "dev-secret", "video")
```

Same interface as NewClient — module code doesn't change.

## Emit Events

### Client.Emit

```go
func (c *Client) Emit(ctx context.Context, eventType string, payload any) error
```

Fire-and-forget publish event to all subscribers.

```go
err := ec.Emit(ctx, "transcode_completed", map[string]any{
	"videoId": id,
	"quality": "1080p",
	"url":     downloadURL,
})
```

Behavior:
- Returns immediately (async)
- Platform delivers to all subscribers
- Automatic retries on failure
- Payload limit: 200KB

Event received as: `"video.transcode_completed"`

## Call Other Module

### Client.Call

```go
func (c *Client) Call(ctx context.Context, module, version, route string, request any, response any) error
```

Synchronous request/response to another module.

```go
var resp struct {
	Success    bool `json:"success"`
	NewBalance int  `json:"new_balance"`
}

err := ec.Call(ctx, "credit", "v1", "/deduct", map[string]any{
	"userId": userID,
	"amount": 100,
}, &resp)

if err != nil {
	// Handle error
	return
}

// Use resp.NewBalance
```

Parameters:
- `module` — Target module ID
- `version` — API version
- `route` — HTTP path (must start with /)
- `request` — JSON-serializable request
- `response` — Pointer to response struct

Error handling:
- Returns `CallError` if target returns 4xx/5xx
- Returns error if timeout or network failure

## Run ECS Task

### Client.RunTask

```go
func (c *Client) RunTask(ctx context.Context, taskType string, payload any) (*RunTaskResult, error)
```

Launch long-running operation (> 15 minutes).

```go
result, err := ec.RunTask(ctx, "transcode", map[string]any{
	"videoId": videoID,
	"quality": []string{"720p", "1080p"},
})

if err != nil {
	handler.InternalError(w)
	return
}

handler.WriteJSON(w, 202, map[string]string{
	"taskId": result.TaskID,
})
```

Returns:
```go
type RunTaskResult struct {
	TaskID string
}
```

Payload limit: 5MB

## Register Event Handlers

### Register

```go
func Register(r *chi.Mux, handlers map[string]HandlerFunc) error
```

Subscribe to events from other modules. Adds `/events/*` route with internal auth.

```go
event.Register(r, map[string]event.HandlerFunc{
	"oauth.user_created": h.OnUserCreated,
	"oauth.user_deleted": h.OnUserDeleted,
})
```

Automatically:
- Adds `X-MS-Auth-Type: internal` requirement
- Routes requests to `/events/<type>`
- Validates event structure
- Handles authentication

### HandlerFunc

```go
type HandlerFunc func(w http.ResponseWriter, r *http.Request, evt Event)
```

Handler for incoming events.

```go
func (h *Handler) OnUserCreated(w http.ResponseWriter, r *http.Request, evt event.Event) {
	var payload struct {
		UserID string `json:"user_id"`
		Email  string `json:"email"`
	}

	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		handler.BadRequest(w, "invalid payload")
		return
	}

	// Initialize user
	h.service.InitUser(r.Context(), payload.UserID)

	// Always respond 200
	handler.WriteJSON(w, 200, map[string]bool{"ok": true})
}
```

Requirements:
- Must return HTTP 200 on success
- Must return within 15 seconds
- Failure = platform retries
- Use request context for storage/events

### Event

```go
type Event struct {
	ID        string          // Unique event ID
	Type      string          // "module.event_name"
	AppID     string          // Application ID
	Source    string          // Source module ID
	Payload   json.RawMessage // Event data (JSON)
	Timestamp time.Time       // Emission time
}
```

## Error Handling

### CallError

```go
type CallError struct {
	StatusCode int
	Body       string
	FuncError  string
}

func (e *CallError) Error() string
```

Returned when Call to another module fails.

```go
var callErr *event.CallError
if errors.As(err, &callErr) {
	switch callErr.StatusCode {
	case 402:
		// Insufficient credits
		handler.BadRequest(w, "insufficient credits")
	case 404:
		// Not found
		handler.NotFound(w, "module not available")
	case 500:
		// Server error
		handler.InternalError(w)
	}
}
```

## Payload Limits

| Operation | Limit | Reason |
|-----------|-------|--------|
| Emit | 200KB | Lambda async invoke limit |
| Call | 5MB | Lambda sync invoke limit |
| RunTask | 5MB | ECS task environment limit |

## Local Development

Use `NewHTTPClient` with local platform:

```go
if os.Getenv("AWS_LAMBDA_FUNCTION_NAME") != "" {
	// Production
	cfg, _ := awsconfig.LoadDefaultConfig(ctx)
	ec = event.NewClient(awslambda.NewFromConfig(cfg),
		os.Getenv("PLATFORM_ARN"), "video")
} else {
	// Local dev
	ec = event.NewHTTPClient("http://localhost:3000", "dev-secret", "video")
}
```

The local platform:
- Routes Emit to `/internal/invoke`
- Routes Call to `/internal/invoke`
- Delivers events to subscribers
- Simulates ECS tasks

## Usage Patterns

### Publish-Subscribe

```go
// Publisher (video module)
ec.Emit(ctx, "transcode_completed", map[string]any{
	"videoId": id,
	"url": url,
})

// Subscriber (notification module)
event.Register(r, map[string]event.HandlerFunc{
	"video.transcode_completed": h.OnTranscodeCompleted,
})
```

### Request-Response

```go
// Caller (video module)
var balance int
ec.Call(ctx, "credit", "v1", "/get-balance", map[string]any{
	"userId": userID,
}, &balance)

// Handler (credit module)
func (h *Handler) GetBalance(w http.ResponseWriter, r *http.Request) {
	var req map[string]string
	handler.DecodeJSON(w, r, &req)
	balance := h.service.GetBalance(r.Context(), req["userId"])
	handler.WriteJSON(w, 200, balance)
}
```

### Task Orchestration

```go
// Main module
result, _ := ec.RunTask(ctx, "transcode", map[string]any{
	"videoId": videoID,
})

// ECS task receives via environment + can use:
ctx := handler.NewContext(context.Background(), handler.ContextParams{
	AppID: os.Getenv("APP_ID"),
	// ...
})
ec.Emit(ctx, "transcode_progress", map[string]any{
	"videoId": videoID,
	"progress": 50,
})
```

## See Also

- [Event Guide](../guides/event.md)
- [Full Module Example](../examples/full-module.md)
- [ECS Task Example](../examples/ecs-task.md)
