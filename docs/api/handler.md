---
title: handler Package API Reference
description: Context, auth, JSON, pagination, logging, SSE functions
order: 1
---

## Context Management

### ExtractContext

```go
func ExtractContext(next http.Handler) http.Handler
```

Chi middleware that reads X-MS-* headers into request context. Must be the first middleware.

```go
r := chi.NewRouter()
r.Use(handler.ExtractContext)
```

### NewContext

```go
func NewContext(parent context.Context, p ContextParams) context.Context
```

Build a context outside of HTTP requests (ECS tasks, cron jobs).

```go
ctx := handler.NewContext(context.Background(), handler.ContextParams{
	AppID:      os.Getenv("APP_ID"),
	SchemaName: os.Getenv("SCHEMA_NAME"),
	ModuleID:   os.Getenv("MODULE_ID"),
	AuthType:   handler.AuthTypeInternal,
})
```

### ContextParams

```go
type ContextParams struct {
	AppID       string
	SchemaName  string
	AppPublicID string
	RequestID   string
	ModuleID    string
	AuthType    string
}
```

## Context Getters

All return empty string if not set.

```go
func GetAppID(ctx context.Context) string
func GetSchemaName(ctx context.Context) string
func GetAppPublicID(ctx context.Context) string
func GetRequestID(ctx context.Context) string
func GetPlatformUserID(ctx context.Context) string
func GetPlatformUserPublicID(ctx context.Context) string
func GetModuleID(ctx context.Context) string
func GetAuthType(ctx context.Context) string
```

## Authentication

### RequirePlatformUser

```go
func RequirePlatformUser(permissions ...platformPermission) func(http.Handler) http.Handler
```

Middleware requiring authenticated platform user. Returns 401 if not authenticated.

Optionally specify required permissions:
- `PlatformRead` — User has read access
- `PlatformWrite` — User has write access
- `PlatformAdmin` — User has admin access

```go
r.Group(func(r chi.Router) {
	r.Use(handler.RequirePlatformUser())  // Any authenticated user
	r.Get("/stats", h.Stats)
})

r.Group(func(r chi.Router) {
	r.Use(handler.RequirePlatformUser(handler.PlatformAdmin))  // Admin only
	r.Post("/config", h.UpdateConfig)
})
```

### RequireInternal

```go
func RequireInternal(next http.Handler) http.Handler
```

Middleware requiring `X-MS-Auth-Type: internal`. Used for event handlers and internal-only routes.

Automatically applied by `event.Register()`.

### Permission Constants

```go
const (
	PlatformRead  platformPermission
	PlatformWrite platformPermission
	PlatformAdmin platformPermission
)
```

Use with `RequirePlatformUser()` to enforce permission levels.

### AuthType Constants

```go
const (
	AuthTypeUser     = "user"
	AuthTypeInternal = "internal"
)
```

## JSON Encoding/Decoding

### DecodeJSON

```go
func DecodeJSON(w http.ResponseWriter, r *http.Request, v any) error
```

Decode and validate JSON request body (1MB limit). Writes error response on failure.

```go
var req CreateVideoRequest
if err := handler.DecodeJSON(w, r, &req); err != nil {
	return  // Error response already written
}
```

Features:
- 1MB size limit
- Validates JSON structure
- Rejects trailing data
- Writes error response automatically

### WriteJSON

```go
func WriteJSON(w http.ResponseWriter, statusCode int, v any) error
```

Encode and write JSON response with proper headers.

```go
handler.WriteJSON(w, 200, video)
handler.WriteJSON(w, 201, newVideo)
```

Sets `Content-Type: application/json`.

## Error Responses

All error functions write JSON with `error` field:
```json
{"error": "message"}
```

### BadRequest

```go
func BadRequest(w http.ResponseWriter, message string)
```

Write 400 response.

### NotFound

```go
func NotFound(w http.ResponseWriter, message string)
```

Write 404 response.

### Forbidden

```go
func Forbidden(w http.ResponseWriter, message string)
```

Write 403 response.

### Unauthorized

```go
func Unauthorized(w http.ResponseWriter, message string)
```

Write 401 response.

### InternalError

```go
func InternalError(w http.ResponseWriter)
```

Write 500 response. Message is omitted for security.

## Pagination

### ParsePagination

```go
func ParsePagination(r *http.Request) Pagination
```

Parse `?limit=N&offset=M` query parameters.

```go
type Pagination struct {
	Limit  int
	Offset int
}
```

Defaults:
- `limit`: 10
- `max limit`: 100
- `offset`: 0

```go
p := handler.ParsePagination(r)
items, total := h.service.List(ctx, p.Limit, p.Offset)
```

### NewPaginatedResponse

```go
func NewPaginatedResponse[T any](data []T, total int, p Pagination) PaginatedResponse[T]
```

Build paginated response object.

```go
type PaginatedResponse[T any] struct {
	Data    []T  `json:"data"`
	Total   int  `json:"total"`
	Limit   int  `json:"limit"`
	Offset  int  `json:"offset"`
	HasMore bool `json:"has_more"`
}
```

Usage:

```go
handler.WriteJSON(w, 200, handler.NewPaginatedResponse(videos, total, p))
```

Response example:
```json
{
	"data": [...],
	"total": 50,
	"limit": 10,
	"offset": 0,
	"has_more": true
}
```

## Logging

### InitLogger

```go
func InitLogger()
```

Initialize logger. Call once in `main()`.

Auto-detects runtime:
- Lambda → JSON structured logs
- Local → Human-readable text

### Logger

```go
func Logger(ctx context.Context) *slog.Logger
```

Get logger for structured logging. Auto-attaches:
- `request_id`
- `app_id`
- `module_id`
- `user_id` (if authenticated)

```go
handler.Logger(ctx).Info("video uploaded",
	"videoId", id,
	"duration", duration,
)
```

Log levels: Debug, Info, Warn, Error

### RequestLogger

```go
func RequestLogger(next http.Handler) http.Handler
```

Middleware that logs HTTP requests. Should be added after ExtractContext.

```go
r.Use(handler.ExtractContext)
r.Use(handler.RequestLogger)
```

Logs:
- Request method and path
- Status code
- Response time
- Errors

## Server-Sent Events

### NewSSE

```go
func NewSSE(w http.ResponseWriter, r *http.Request) *SSE
```

Create SSE writer for streaming responses.

```go
sse := handler.NewSSE(w, r)
sse.Send("progress", map[string]any{"status": "analyzing"})
```

### SSE.IsReconnect

```go
func (s *SSE) IsReconnect() bool
```

Check if client reconnected (after disconnect).

```go
if sse.IsReconnect() {
	// Resume from stored state
	return
}
```

### SSE.Send

```go
func (s *SSE) Send(eventType string, data any) error
```

Send an event to client.

```go
sse.Send("progress", map[string]any{"status": "50%", "eta": "2m"})
sse.Send("done", map[string]any{"url": result.URL})
```

## Health Check

### HealthCheck

```go
func HealthCheck() http.HandlerFunc
```

Handler for GET /health endpoint. Returns 200 with `{"status":"ok"}`.

```go
r.Get("/health", module.HealthCheck())
```

## Headers

Standard headers read by ExtractContext:

```go
const (
	HeaderAppID              = "X-MS-App-ID"
	HeaderSchemaName         = "X-MS-Schema-Name"
	HeaderAppPublicID        = "X-MS-App-Public-ID"
	HeaderRequestID          = "X-MS-Request-ID"
	HeaderPlatformUserID     = "X-MS-Platform-User-ID"
	HeaderPlatformUserPublicID = "X-MS-Platform-User-Public-ID"
	HeaderModuleID           = "X-MS-Module-ID"
	HeaderAuthType           = "X-MS-Auth-Type"
	HeaderInternalSecret     = "X-MS-Internal-Secret"
)
```

## See Also

- [Handler Guide](../guides/handler.md)
- [Full Module Example](../examples/full-module.md)
