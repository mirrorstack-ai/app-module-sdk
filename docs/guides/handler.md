---
title: HTTP Handlers & Context
description: Request context, auth, JSON, pagination, logging, and SSE
order: 1
---

## Context Extraction

The `handler.ExtractContext` middleware reads X-MS-* headers injected by Core API and populates the request context.

```go
r := chi.NewRouter()
r.Use(handler.ExtractContext)  // Must be first middleware
```

### Getting Context Values

```go
appID := handler.GetAppID(ctx)
schema := handler.GetSchemaName(ctx)
userID := handler.GetPlatformUserID(ctx)
moduleID := handler.GetModuleID(ctx)
requestID := handler.GetRequestID(ctx)
```

## Authentication

### Require Platform User

Protect admin endpoints that only app owners can access:

```go
r.Group(func(r chi.Router) {
	r.Use(handler.RequirePlatformUser())
	r.Get("/admin/stats", h.AdminStats)
	r.Post("/admin/config", h.UpdateConfig)
})
```

### Require Internal

Automatic for event handlers. Validates `X-MS-Auth-Type: internal`:

```go
// No need to add manually — event.Register() adds this automatically
event.Register(r, map[string]event.HandlerFunc{
	"oauth.user_created": h.OnUserCreated,
})
```

### Custom Auth

```go
func requireSubscription(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Your auth logic
		if !hasActiveSubscription(r.Context()) {
			handler.Forbidden(w, "subscription required")
			return
		}
		next.ServeHTTP(w, r)
	})
}
```

## JSON Requests & Responses

### Decode JSON

```go
var req struct {
	Title    string `json:"title"`
	Duration int    `json:"duration"`
}

if err := handler.DecodeJSON(w, r, &req); err != nil {
	return  // handler.DecodeJSON writes error response
}

// req is now populated
```

Properties:
- 1MB size limit
- Rejects trailing data
- Validates JSON structure

### Write JSON Response

```go
handler.WriteJSON(w, http.StatusOK, video)
// Automatically sets Content-Type: application/json
```

### Error Responses

```go
// 400 Bad Request
handler.BadRequest(w, "title is required")

// 404 Not Found
handler.NotFound(w, "video not found")

// 403 Forbidden
handler.Forbidden(w, "insufficient permissions")

// 500 Internal Error
handler.InternalError(w)
```

All error helpers write JSON responses with `error` field.

## Pagination

### Parse Pagination

```go
p := handler.ParsePagination(r)  // ?limit=10&offset=30
// p.Limit defaults to 10, max 100
// p.Offset defaults to 0
```

### Build Paginated Response

```go
videos, total := h.service.List(ctx, p.Limit, p.Offset)
handler.WriteJSON(w, 200, handler.NewPaginatedResponse(videos, total, p))
```

Response structure:
```json
{
	"data": [...],
	"total": 50,
	"limit": 10,
	"offset": 30,
	"has_more": true
}
```

## Logging

### Initialize Logger

Call once in `main()`:

```go
handler.InitLogger()
```

Auto-detects runtime:
- Lambda → JSON structured logs
- Local dev → Human-readable text

### Structured Logging

```go
handler.Logger(ctx).Info("video uploaded",
	"videoId", id,
	"duration", duration,
)
```

Auto-attached fields (from context):
- `request_id`
- `app_id`
- `module_id`
- `user_id` (if authenticated)

Log levels: Debug, Info, Warn, Error

## Server-Sent Events (SSE)

Stream progress for long-running operations (< 15 minutes).

### Start SSE

```go
func (h *Handler) Transcode(w http.ResponseWriter, r *http.Request) {
	sse := handler.NewSSE(w, r)

	if sse.IsReconnect() {
		// Client reconnected after disconnect
		state := h.storage.GetJobState(ctx, jobID)
		sse.Send("progress", state)
		return
	}

	// Start operation
	sse.Send("progress", map[string]any{"status": "analyzing"})
	result := h.transcode(ctx, videoID)

	sse.Send("done", map[string]any{"url": result.URL})
}
```

### SSE Best Practices

- Use for operations < 15 minutes
- For longer tasks, use `event.RunTask()` instead
- Send progress updates at least every 30 seconds
- Include a way for clients to resume from stored state

## Non-HTTP Context

For ECS tasks, cron jobs, and CLI tools that need to emit events or access storage:

```go
ctx := handler.NewContext(context.Background(), handler.ContextParams{
	AppID:      os.Getenv("APP_ID"),
	SchemaName: os.Getenv("SCHEMA_NAME"),
	ModuleID:   os.Getenv("MODULE_ID"),
	AuthType:   handler.AuthTypeInternal,
})

// Now can use storage.WithSchema, event.Emit, etc.
```

## Request ID Correlation

Every request gets a unique `X-MS-Request-ID` header. Use it to correlate logs and traces:

```go
requestID := handler.GetRequestID(ctx)
handler.Logger(ctx).Info("starting operation", "requestId", requestID)
```

## Full Handler Example

```go
type VideoHandler struct {
	pool *pgxpool.Pool
	ec   *event.Client
}

func (h *VideoHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := handler.ParsePagination(r)

	videos, total, err := storage.WithSchema(ctx, h.pool,
		handler.GetSchemaName(ctx), func(tx pgx.Tx) ([]Video, int, error) {
		return queries.ListVideos(ctx, tx, p.Limit, p.Offset)
	})

	if err != nil {
		handler.Logger(ctx).Error("list failed", "error", err)
		handler.InternalError(w)
		return
	}

	handler.WriteJSON(w, 200,
		handler.NewPaginatedResponse(videos, total, p))
}

func (h *VideoHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title string `json:"title"`
	}

	if err := handler.DecodeJSON(w, r, &req); err != nil {
		return
	}

	if req.Title == "" {
		handler.BadRequest(w, "title is required")
		return
	}

	ctx := r.Context()
	video := &Video{ID: uuid.New().String(), Title: req.Title}

	err := storage.WithSchema(ctx, h.pool,
		handler.GetSchemaName(ctx), func(tx pgx.Tx) error {
		return queries.CreateVideo(ctx, tx, video)
	})

	if err != nil {
		handler.InternalError(w)
		return
	}

	handler.Logger(ctx).Info("video created", "videoId", video.ID)
	handler.WriteJSON(w, 201, video)
}
```

## See Also

- [handler Package Reference](../api/handler.md)
- [Event Communication](./event.md)
- [Storage Access](./storage.md)
