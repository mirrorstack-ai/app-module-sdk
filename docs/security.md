---
title: Security Features & Best Practices
description: Built-in protections, validation, and secure patterns
order: 99
---

## Authentication & Authorization

### Platform-Managed Auth

Authentication is handled by Core API:
- Issues `X-MS-Platform-User-ID` header when user is authenticated
- Returns empty header for unauthenticated requests
- Modules receive pre-validated headers

### User Routes (Authenticated)

```go
userID := handler.GetPlatformUserID(ctx)
if userID == "" {
	// User not authenticated
	handler.Forbidden(w, "authentication required")
	return
}
```

### Admin Routes (Platform User)

```go
r.Use(handler.RequirePlatformUser())
// Returns 401 if user not authenticated or not app owner
```

### Internal Routes (Event Handlers)

```go
event.Register(r, handlers)
// Automatically requires X-MS-Auth-Type: internal
```

Custom internal route:

```go
r.Use(handler.RequireInternal)
r.Post("/internal-only", h.InternalHandler)
```

## Data Validation

### Schema Isolation

Schemas are validated to prevent injection:

```go
// Valid: app_user_data, app_123
// Invalid: public, users, app-test, app test
var validSchema = regexp.MustCompile(`\Aapp_[a-z0-9_]{1,63}\z`)
```

Schema names are sanitized:
```go
// Safe from SQL injection
tx.Exec(ctx, "SET LOCAL search_path TO "+pgx.Identifier{schema}.Sanitize())
```

### JSON Validation

```go
var req CreateRequest
if err := handler.DecodeJSON(w, r, &req); err != nil {
	return  // Validation failed, error written
}

// Properties:
// - 1MB size limit (prevents DoS)
// - Rejects malformed JSON
// - Rejects trailing data
```

### File Path Validation

File paths are validated against:
- `..` (directory traversal)
- URL encoding tricks
- Null bytes
- Absolute paths

```go
// These reject:
fc.PresignPut(ctx, "../etc/passwd", ...)           // .. detected
fc.PresignPut(ctx, "..%2fetc%2fpasswd", ...)       // URL encoding
fc.PresignPut(ctx, "/etc/passwd", ...)             // Absolute path
fc.PresignPut(ctx, "file\x00.txt", ...)            // Null byte

// Local backend containment check:
// All paths must fall within /tmp/mirrorstack/applications/{appID}/{moduleID}/
```

## Request Security

### Body Size Limits

- JSON requests: 1MB limit
- Lambda Emit payload: 200KB limit
- Lambda Call payload: 5MB limit
- ECS task payload: 5MB limit

```go
// DecodeJSON enforces 1MB
if err := handler.DecodeJSON(w, r, &req); err != nil {
	// Limits enforced
	return
}
```

### Header Timeouts

```go
ReadHeaderTimeout: 5 * time.Second    // Slowloris protection
ReadTimeout:      30 * time.Second
WriteTimeout:     60 * time.Second
IdleTimeout:      120 * time.Second
MaxHeaderBytes:   32 * 1024           // 32KB header limit
```

### HTTPS Enforcement

Core API enforces HTTPS. Modules should:
- Trust `X-Forwarded-Proto` header only when behind Core API
- Log suspicious requests (non-HTTPS forwarded requests)
- Never rely on headers for auth (use X-MS-* headers)

## Storage Security

### Multi-Tenant Isolation

Each app has separate PostgreSQL schema:

```go
// Cannot cross schemas
storage.WithSchema(ctx, pool, "app_user1", func(tx pgx.Tx) {
	// Cannot access app_user2 tables
})

// SQL injection prevention:
// - Schema names validated and sanitized
// - All data parameters use prepared statements
```

### Key Namespace Isolation

Redis keys are automatically prefixed:

```
mirrorstack-{stage}:applications:{appID}:{moduleID}:{key}
```

Cannot access keys from other apps/modules:

```go
// Module's cache:
cc.Set(ctx, "secret", value, ttl)
// Actual Redis key: mirrorstack-prod:applications:app-1:video:secret

// Cannot access:
redisClient.Get(ctx, "mirrorstack-prod:applications:app-2:video:secret")
```

### File Storage Isolation

S3 paths are automatically prefixed:

```
applications/{appID}/{moduleID}/{path}
```

Module cannot access other apps/modules' files:

```go
// Module's file:
fc.PresignPut(ctx, "videos/upload.mp4", ...)
// Actual S3 path: applications/app-1/video/videos/upload.mp4

// Cannot directly access:
fc.PresignPut(ctx, "../../app-2/video/sensitive.mp4", ...)
// Path validation prevents this
```

### Presigned URL Security

- **TTL limits** — Max 7 days (S3 SigV4 limit)
- **Signature verification** — S3 validates request matches signature
- **Time-based expiry** — URLs expire after TTL

```go
// Safe presigned URL
result, _ := fc.PresignPut(ctx, "file.mp4", 15*time.Minute, "video/mp4")
// Client has 15 minutes to upload
// After 15 minutes, URL is invalid
```

## Environment Security

### Sensitive Env Vars

Modules receive these via secure injection:
- `DATABASE_URL` — Connection string (passed securely)
- `REDIS_ADDR` — Cache endpoint
- `S3_BUCKET` — Bucket name
- `PLATFORM_ARN` — Lambda function ARN

### Secrets Management

Modules should NOT store:
- API keys in code
- Database passwords hardcoded
- AWS credentials

Instead:
- Use IAM roles (Lambda execution role)
- Inject via secure env vars (platform)
- Never log sensitive data

```go
// WRONG
password := "super-secret-password"

// RIGHT — from environment
password := os.Getenv("DATABASE_PASSWORD")

// NEVER log sensitive data
handler.Logger(ctx).Info("connected", "password", password) // WRONG
handler.Logger(ctx).Info("connected to database")           // OK
```

## Event Security

### Event Authentication

Event handlers automatically require internal auth:

```go
event.Register(r, handlers)
// All /events/* routes require X-MS-Auth-Type: internal
```

Only Core API and internal modules can invoke.

### Payload Validation

```go
func (h *Handler) OnEvent(w http.ResponseWriter, r *http.Request, evt event.Event) {
	var payload struct {
		UserID string `json:"user_id"`
	}

	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		// Validate structure
		handler.BadRequest(w, "invalid event payload")
		return
	}

	// Validate data
	if payload.UserID == "" {
		handler.BadRequest(w, "user_id required")
		return
	}
}
```

### Event Rate Limiting

Implement rate limiting if needed:

```go
func rateLimitMiddleware(maxPerMinute int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		limiter := rate.NewLimiter(rate.Limit(maxPerMinute)/60, 1)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !limiter.Allow() {
				handler.WriteJSON(w, 429, map[string]string{
					"error": "too many requests",
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
```

## Logging Security

### No Sensitive Data in Logs

```go
// WRONG
handler.Logger(ctx).Info("user login",
	"userId", userID,
	"password", password,  // NEVER
	"sessionToken", token, // NEVER
)

// RIGHT
handler.Logger(ctx).Info("user login", "userId", userID)

// Mask PII in logs
masked := strings.Repeat("*", len(email)-4) + email[len(email)-4:]
handler.Logger(ctx).Info("user signup", "email", masked)
```

### Structured Logging

Use structured logging to enable filtering:

```go
handler.Logger(ctx).Info("action",
	"action", "delete_video",
	"userId", userID,
	"resourceId", videoID,
)

// Logs can be filtered: grep 'action' logs | grep delete_video
```

## Rate Limiting & DDoS Protection

### Implement Rate Limiting

Use library like `golang.org/x/time/rate`:

```go
func rateLimitMiddleware(appID string) func(http.Handler) http.Handler {
	limiters := make(map[string]*rate.Limiter)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID := handler.GetPlatformUserID(r.Context())
			if userID == "" {
				userID = r.RemoteAddr
			}

			limiter, ok := limiters[userID]
			if !ok {
				limiter = rate.NewLimiter(100, 10) // 100 req/sec, burst 10
				limiters[userID] = limiter
			}

			if !limiter.Allow() {
				handler.WriteJSON(w, 429, map[string]string{
					"error": "rate limited",
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
```

### Query Timeouts

Always set reasonable timeouts:

```go
ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
defer cancel()

videos, err := h.service.ListVideos(ctx, limit, offset)
```

## SQL Injection Prevention

### Use Prepared Statements

All SDK storage operations use prepared statements:

```go
// Safe — parameterized
queries.GetVideo(ctx, tx, videoID)

// WRONG — string interpolation
fmt.Sprintf("SELECT * FROM videos WHERE id = '%s'", videoID)
```

Schema names are specially protected:

```go
// Safe — uses pgx.Identifier.Sanitize()
storage.WithSchema(ctx, pool, schema, fn)

// WRONG
query := fmt.Sprintf("SET search_path TO %s", schema)
```

## Testing Security

### Test Isolation

```go
func TestSchemaIsolation(t *testing.T) {
	ctx := handler.NewContext(context.Background(), handler.ContextParams{
		AppID:      "app-1",
		SchemaName: "app_1",
		ModuleID:   "video",
	})

	// Verify isolation: should not access app_2 schema
	err := storage.WithSchema(ctx, pool, "app_2", fn)
	// Should fail or be rejected
}
```

### Test Secret Handling

Never commit secrets:

```bash
# .gitignore
.env
.env.local
secrets/
```

Use test fixtures:

```go
func TestHandler(t *testing.T) {
	testCtx := handler.NewContext(context.Background(), handler.ContextParams{
		AppID:      "test-app",
		SchemaName: "app_test",
		ModuleID:   "test-module",
	})

	// Run test with isolated context
	h := &Handler{pool: testPool}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/videos", nil).WithContext(testCtx)
	h.ListVideos(w, r)
}
```

## Security Checklist

- [ ] No hardcoded secrets in code or config
- [ ] All sensitive data validated before use
- [ ] Rate limiting on user-facing endpoints
- [ ] File paths validated (no path traversal)
- [ ] Schema names validated and sanitized
- [ ] No logging of PII or secrets
- [ ] All SQL queries use parameterized statements
- [ ] Request timeouts configured
- [ ] Event handlers validate payloads
- [ ] Authentication checked on protected routes
- [ ] HTTPS enforced (via Core API)
- [ ] Tests verify isolation (schema, keys, files)

## See Also

- [handler Package API](./api/handler.md)
- [event Package API](./api/event.md)
- [storage Package API](./api/storage.md)
- [Full Module Example](./examples/full-module.md)
