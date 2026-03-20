---
title: Inter-Module Events
description: Register, Emit, Call, RunTask, and local dev setup
order: 2
---

## Overview

Modules communicate via events:
- **Emit** — Fire-and-forget publish (async)
- **Call** — Synchronous request/response
- **RunTask** — Long-running operations (ECS)
- **Register** — Subscribe to events from other modules

## Setup

### Production (Lambda Invoke)

```go
import (
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/mirrorstack-ai/app-module-sdk/event"
)

cfg, _ := awsconfig.LoadDefaultConfig(ctx)
lambdaClient := awslambda.NewFromConfig(cfg)
ec := event.NewClient(lambdaClient, os.Getenv("PLATFORM_ARN"), "video")
```

### Local Development (HTTP Fallback)

```go
ec := event.NewHTTPClient(
	"http://localhost:3000",    // Core API URL
	"dev-secret",                // Shared secret
	"video",                      // This module's ID
)
```

### Auto-Detection Pattern

```go
var ec *event.Client

if os.Getenv("AWS_LAMBDA_FUNCTION_NAME") != "" {
	// Production: Lambda invoke
	cfg, _ := awsconfig.LoadDefaultConfig(ctx)
	ec = event.NewClient(awslambda.NewFromConfig(cfg),
		os.Getenv("PLATFORM_ARN"), "video")
} else {
	// Development: HTTP fallback
	ec = event.NewHTTPClient("http://localhost:3000", "dev-secret", "video")
}
```

## Register Event Handlers

Subscribe to events from other modules:

```go
event.Register(r, map[string]event.HandlerFunc{
	"oauth.user_created": h.OnUserCreated,
	"oauth.user_deleted": h.OnUserDeleted,
})

func (h *Handler) OnUserCreated(w http.ResponseWriter, r *http.Request, evt event.Event) {
	var payload struct {
		UserID   string `json:"user_id"`
		Email    string `json:"email"`
	}

	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		handler.BadRequest(w, "invalid payload")
		return
	}

	ctx := r.Context()
	// Initialize user balance, profile, etc.
	h.service.InitBalance(ctx, payload.UserID)

	// Always respond with 200 OK
	handler.WriteJSON(w, 200, map[string]bool{"ok": true})
}
```

### Event Structure

```go
type Event struct {
	ID        string          // Unique event ID
	Type      string          // "module.event_name"
	AppID     string          // Application ID
	Source    string          // Source module ID
	Payload   json.RawMessage // Event data
	Timestamp time.Time       // When emitted
}
```

### Handler Requirements

- Must return HTTP 200 on success
- Must return quickly (< 15 seconds)
- Failure to respond = Core API retries
- Use structured data in `Payload`

## Emit Events

Fire-and-forget publish to all subscribers:

```go
err := ec.Emit(ctx, "transcode_completed", map[string]any{
	"videoId": id,
	"quality": "1080p",
	"url":     downloadURL,
})

if err != nil {
	handler.Logger(ctx).Error("emit failed", "error", err)
	// Consider: retry, log, or ignore
}
```

### Event Naming

Events are namespaced by module:
- Emit: `"transcode_completed"`
- Received as: `"video.transcode_completed"`

### Async Behavior

- Returns immediately
- Platform delivers to all subscribers
- Failed deliveries are retried by platform

## Call Another Module

Synchronous request/response to another module:

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
	handler.Logger(ctx).Error("call failed", "error", err)
	handler.InternalError(w)
	return
}

handler.WriteJSON(w, 200, map[string]int{"balance": resp.NewBalance})
```

### Call Parameters

- `module` — Target module ID
- `version` — API version (e.g., "v1")
- `route` — HTTP path (e.g., "/deduct")
- `request` — JSON-serializable request body
- `response` — Pointer to response struct

### Error Handling

```go
var callErr *event.CallError
if errors.As(err, &callErr) {
	if callErr.StatusCode == 402 {
		// Insufficient credits
		handler.BadRequest(w, "insufficient credits")
		return
	}
	if callErr.StatusCode == 500 {
		// Target module error
		handler.InternalError(w)
		return
	}
}
```

## Run ECS Task

Launch long-running operations (> 15 minutes):

```go
result, err := ec.RunTask(ctx, "transcode", map[string]any{
	"videoId": videoID,
	"quality": []string{"720p", "1080p"},
})

if err != nil {
	handler.InternalError(w)
	return
}

// Return task ID for client polling
handler.WriteJSON(w, 202, map[string]string{
	"taskId": result.TaskID,
	"statusUrl": fmt.Sprintf("/tasks/%s", result.TaskID),
})
```

### Task Lifecycle

1. Module emits `RunTask`
2. Platform spins up ECS task with payload
3. Task runs with same environment variables as module
4. Task emits events or calls other modules
5. Client polls task status

### Task Handler Pattern

The ECS task receives same environment:
- `APP_ID`
- `SCHEMA_NAME`
- `MODULE_ID`

Use `handler.NewContext()` to create context:

```go
func main() {
	ctx := handler.NewContext(context.Background(), handler.ContextParams{
		AppID:      os.Getenv("APP_ID"),
		SchemaName: os.Getenv("SCHEMA_NAME"),
		ModuleID:   os.Getenv("MODULE_ID"),
		AuthType:   handler.AuthTypeInternal,
	})

	// Can now use storage.WithSchema, event.Emit, etc.
	transcodeVideo(ctx, payload)
}
```

## Event Routing

### Route Protection

- `/events/*` routes require `X-MS-Auth-Type: internal`
- Only Core API and internal modules can invoke
- `event.Register()` adds this automatically

### Custom Event Routes

```go
r.Post("/events/internal-only", h.SomeInternalHandler)
// Must manually add: r.Use(handler.RequireInternal)
```

## Local Development Setup

### Minimal Local Platform

Run a local event relay that translates Emit/Call to HTTP:

```bash
# Start your module
PORT=8080 go run .

# Your module uses:
# ec := event.NewHTTPClient("http://localhost:3000", "dev-secret", "video")
```

The local platform at `http://localhost:3000` handles:
- `/internal/invoke` — Route Emit and Call requests
- Event delivery to local handlers
- Task simulation

## Event Examples

### User Signup Flow

```go
// oauth module emits:
ec.Emit(ctx, "user_created", map[string]any{
	"userId": id,
	"email": email,
})

// video module receives and initializes:
func (h *Handler) OnUserCreated(w http.ResponseWriter, r *http.Request, evt event.Event) {
	var payload struct{ UserID string }
	json.Unmarshal(evt.Payload, &payload)

	h.service.InitUserStorage(r.Context(), payload.UserID)
	handler.WriteJSON(w, 200, map[string]bool{"ok": true})
}
```

### Credit Deduction

```go
// video module calls credit module:
var resp struct{ NewBalance int }
ec.Call(ctx, "credit", "v1", "/deduct", map[string]any{
	"userId": userID,
	"amount": 10,
}, &resp)

// credit module responds:
func (h *Handler) Deduct(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID string `json:"user_id"`
		Amount int    `json:"amount"`
	}
	handler.DecodeJSON(w, r, &req)

	balance, err := h.service.Deduct(r.Context(), req.UserID, req.Amount)
	if err != nil {
		handler.BadRequest(w, "insufficient credits")
		return
	}

	handler.WriteJSON(w, 200, map[string]int{"newBalance": balance})
}
```

## See Also

- [event Package Reference](../api/event.md)
- [Full Module Example](../examples/full-module.md)
- [ECS Task Example](../examples/ecs-task.md)
