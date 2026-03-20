---
title: Storage: Database, Files, Cache
description: Multi-tenant database, S3/local files, Redis caching
order: 3
---

## Database Access

### Multi-Tenant Isolation

Each application has its own PostgreSQL schema. `WithSchema` automatically sets search_path:

```go
videos, err := storage.WithSchema(ctx, pool, schema, func(tx pgx.Tx) ([]Video, error) {
	return queries.ListVideos(ctx, tx)
})
```

### Pattern: WithSchema + sqlc

```go
// Define your sqlc queries in sql/queries.sql:
-- name: ListVideos :many
-- SELECT * FROM videos WHERE app_id = $1 ORDER BY created_at DESC

// sqlc generates queries.go with:
// func ListVideos(ctx context.Context, tx pgx.Tx) ([]Video, error)

// In handler:
videos, err := storage.WithSchema(ctx, h.pool, schema, func(tx pgx.Tx) ([]Video, error) {
	return queries.ListVideos(ctx, tx)
})
```

### Schema Validation

Schema names must match: `app_[a-z0-9_]{1,63}`

Invalid schemas are rejected:
- `app_123` — rejected (must start with letter)
- `app_test-db` — rejected (no hyphens)
- `app_my_database` — accepted

## Module Configuration

Store per-module settings in the platform-managed config table:

```go
config, err := storage.GetModuleConfig(ctx, pool, appID, moduleID)
if errors.Is(err, storage.ErrNotFound) {
	// Config not yet created
	return
}

// config is map[string]any
transcodeQuality := config["transcode_quality"].(string)
```

### Config Management

Configuration is typically set via dashboard or admin API, not in module code.

## File Storage

### Production (S3)

```go
import "github.com/aws/aws-sdk-go-v2/service/s3"

s3Client := s3.NewFromConfig(cfg)
presignClient := s3.NewPresignClient(s3Client)
fc := storage.NewFileClient(storage.FileClientConfig{
	Presigner: presignClient,
	Deleter:   s3Client,
	Notifier:  edgeNotifier,  // R2 edge sync
	Bucket:    "my-bucket",
	AppID:     appID,
	ModuleID:  moduleID,
})
```

### Local Development

```go
fc := storage.NewLocalFileClient(
	"/tmp/mirrorstack",           // Local storage dir
	"http://localhost:9000",      // Presign base URL
	appID,
	moduleID,
)
```

### Upload (Presigned URL)

Client uploads directly to S3 using presigned URL:

```go
result, err := fc.PresignPut(ctx, "videos/raw/upload.mp4", 15*time.Minute, "video/mp4")
if err != nil {
	handler.InternalError(w)
	return
}

// Return URL to client
handler.WriteJSON(w, 200, map[string]string{"uploadUrl": result.URL})

// Client: PUT <uploadUrl> with video data
// Server: Webhook notifies when upload completes
```

### Download (Presigned URL)

```go
result, err := fc.PresignGet(ctx, "videos/processed/video.mp4", 1*time.Hour)
if err != nil {
	handler.NotFound(w, "video not found")
	return
}

handler.WriteJSON(w, 200, map[string]string{"downloadUrl": result.URL})
```

### Delete

Permanent deletion:

```go
err := fc.Delete(ctx, "videos/raw/old.mp4")
```

### Soft Delete (Recoverable)

Temporarily remove file (recoverable within retention period):

```go
err := fc.SoftDelete(ctx, "videos/raw/old.mp4")

// Later: restore if needed
err := fc.Restore(ctx, "videos/raw/old.mp4")
```

### Edge Sync (R2)

Sync frequently-accessed files to edge cache:

```go
err := fc.Sync(ctx, "thumbnails/thumb.jpg")
```

Use for:
- Thumbnail images
- Small metadata files
- Public assets

### Path Format

All paths are automatically prefixed:
```
applications/{appID}/{moduleID}/{path}
```

Your code: `fc.PresignGet(ctx, "videos/thumb.jpg")`
Actual S3 path: `applications/app-1/video/videos/thumb.jpg`

## Cache (Redis)

### Production (ElastiCache)

```go
import "github.com/redis/go-redis/v9"

redisClient := redis.NewClient(&redis.Options{
	Addr: os.Getenv("REDIS_ADDR"),
})

cc := storage.NewCacheClient(redisClient, "prod", appID, moduleID)
```

### Local Development

```go
cc := storage.NewLocalCacheClient("dev", appID, moduleID)
// Uses in-memory cache during tests
```

### Set & Get

```go
// Set with TTL
err := cc.Set(ctx, "views:v-123", "42", 5*time.Minute)

// Get
val, err := cc.Get(ctx, "views:v-123")
if errors.Is(err, storage.ErrCacheMiss) {
	// Key not found or expired
	val = "0"
}
```

### Delete

```go
err := cc.Del(ctx, "views:v-123")
```

### Key Format

Keys are automatically prefixed:
```
mirrorstack-{stage}:applications:{appID}:{moduleID}:{key}
```

Your code: `cc.Set(ctx, "views:v-123", "42", 5*time.Minute)`
Actual Redis key: `mirrorstack-prod:applications:app-1:video:views:v-123`

## Auto-Tracked Metrics

When a meter is in the context, storage operations auto-track metrics:

```go
| Operation | Metric |
|-----------|--------|
| WithSchema | db_duration_ms, db_queries |
| CacheClient.Get/Set/Del | cache_get, cache_set, cache_del |
| FileClient.PresignPut | file_upload |
| FileClient.PresignGet | file_download |
| FileClient.Delete | file_delete |
```

[See Metering Guide](./meter.md)

## Pattern: Cache-Aside

Load with cache fallback:

```go
cacheKey := fmt.Sprintf("video:%s", videoID)

// Try cache
val, err := cc.Get(ctx, cacheKey)
if err == nil {
	// Cache hit
	return unmarshal(val)
}

// Cache miss — load from DB
video, err := storage.WithSchema(ctx, pool, schema, func(tx pgx.Tx) (*Video, error) {
	return queries.GetVideo(ctx, tx, videoID)
})

if err != nil {
	return nil, err
}

// Populate cache (fire-and-forget)
go func() {
	cc.Set(context.Background(), cacheKey, marshal(video), 1*time.Hour)
}()

return video, nil
```

## Pattern: Bulk Operations

```go
// Get multiple videos
videos, err := storage.WithSchema(ctx, pool, schema, func(tx pgx.Tx) ([]Video, error) {
	return queries.ListVideos(ctx, tx, ids)
})

// Warm cache in bulk
go func() {
	for _, v := range videos {
		key := fmt.Sprintf("video:%s", v.ID)
		cc.Set(context.Background(), key, marshal(v), 1*time.Hour)
	}
}()
```

## Error Handling

### Common Errors

```go
if errors.Is(err, storage.ErrNotFound) {
	// Key/record doesn't exist
}

if errors.Is(err, storage.ErrCacheMiss) {
	// Cache key expired or missing
}

if errors.Is(err, storage.ErrInvalidPath) {
	// Path validation failed (path traversal, etc.)
}
```

### DB Connection Errors

```go
if errors.Is(err, context.DeadlineExceeded) {
	// Timeout waiting for connection pool
	handler.InternalError(w)
	return
}
```

## Initialization

### In main()

```go
func main() {
	ctx := context.Background()

	// Database
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	// S3 (production) or local (dev)
	var fc storage.FileClient
	if isProduction() {
		cfg, _ := awsconfig.LoadDefaultConfig(ctx)
		s3Client := s3.NewFromConfig(cfg)
		fc = storage.NewFileClient(...)
	} else {
		fc = storage.NewLocalFileClient(...)
	}

	// Redis (production) or local (dev)
	var cc storage.CacheClient
	if isProduction() {
		redisClient := redis.NewClient(...)
		cc = storage.NewCacheClient(redisClient, "prod", appID, moduleID)
	} else {
		cc = storage.NewLocalCacheClient("dev", appID, moduleID)
	}

	h := &Handler{pool, fc, cc}
	module.Start(r)
}
```

## See Also

- [storage Package Reference](../api/storage.md)
- [Metering Guide](./meter.md)
- [Full Module Example](../examples/full-module.md)
