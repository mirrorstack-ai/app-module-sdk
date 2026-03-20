---
title: storage Package API Reference
description: Database, file storage, and cache operations
order: 3
---

## Database

### WithSchema

```go
func WithSchema[T any](ctx context.Context, pool *pgxpool.Pool, schema string,
	fn func(tx pgx.Tx) (T, error)) (T, error)
```

Run function within a transaction with schema isolation. Auto-tracks DB metrics if meter is in context.

```go
video, err := storage.WithSchema(ctx, h.pool, schema, func(tx pgx.Tx) (*Video, error) {
	return queries.GetVideo(ctx, tx, videoID)
})
```

Features:
- Automatic search_path isolation
- Transaction rollback on error
- Schema validation
- Auto-metrics: `db_duration_ms`, `db_queries`

### SetSearchPath

```go
func SetSearchPath(ctx context.Context, tx pgx.Tx, schema string) error
```

Set search_path on existing transaction (for manual transactions).

```go
tx, _ := pool.Begin(ctx)
defer tx.Rollback(ctx)

_ = storage.SetSearchPath(ctx, tx, schema)
// Now tx uses the schema's tables
```

Schema validation: `app_[a-z0-9_]{1,63}`

## Module Configuration

### GetModuleConfig

```go
func GetModuleConfig(ctx context.Context, pool *pgxpool.Pool, appID, moduleID string) (map[string]any, error)
```

Retrieve module configuration from platform-managed table.

```go
config, err := storage.GetModuleConfig(ctx, h.pool, appID, moduleID)
if errors.Is(err, storage.ErrNotFound) {
	// Config not yet created
	return
}

quality := config["transcode_quality"].(string)
```

Returns `storage.ErrNotFound` if config doesn't exist.

## File Storage

### NewFileClient (Production)

```go
func NewFileClient(cfg FileClientConfig) *FileClient
```

Create S3-backed file client (production).

```go
presignClient := s3.NewPresignClient(s3Client)
fc := storage.NewFileClient(storage.FileClientConfig{
	Presigner: presignClient,
	Deleter:   s3Client,
	Notifier:  edgeNotifier,
	Bucket:    "my-bucket",
	AppID:     appID,
	ModuleID:  moduleID,
})
```

### NewLocalFileClient

```go
func NewLocalFileClient(rootDir, presignBaseURL, appID, moduleID string) *FileClient
```

Create filesystem-backed file client (local development).

```go
fc := storage.NewLocalFileClient(
	"/tmp/mirrorstack",
	"http://localhost:9000",
	appID,
	moduleID,
)
```

### FileClient.PresignPut

```go
func (fc *FileClient) PresignPut(ctx context.Context, path string, ttl time.Duration, contentType string) (*PresignResult, error)
```

Get presigned URL for client PUT upload.

```go
result, _ := fc.PresignPut(ctx, "videos/raw/upload.mp4", 15*time.Minute, "video/mp4")
// Return result.URL to client
```

Result:
```go
type PresignResult struct {
	URL     string            // S3 presigned PUT URL
	Headers map[string]string // Signed headers (for PUT)
}
```

Features:
- Client uploads directly to S3
- No server proxy needed
- Auto-tracked: `file_upload`

### FileClient.PresignGet

```go
func (fc *FileClient) PresignGet(ctx context.Context, path string, ttl time.Duration) (*PresignResult, error)
```

Get presigned URL for client GET download.

```go
result, _ := fc.PresignGet(ctx, "videos/processed/video.mp4", 1*time.Hour)
// Return result.URL to client
```

Result:
```go
type PresignResult struct {
	URL     string            // S3 presigned GET URL
	Headers map[string]string // Empty for GET
}
```

Features:
- Client downloads directly from S3
- No bandwidth charges on module
- Auto-tracked: `file_download`

### FileClient.Delete

```go
func (fc *FileClient) Delete(ctx context.Context, path string) error
```

Permanently delete file (including R2 edge copies).

```go
_ = fc.Delete(ctx, "videos/raw/old.mp4")
```

Auto-tracked: `file_delete`

### FileClient.SoftDelete

```go
func (fc *FileClient) SoftDelete(ctx context.Context, path string) error
```

Temporarily mark file as deleted (recoverable).

```go
_ = fc.SoftDelete(ctx, "videos/raw/old.mp4")
```

### FileClient.Restore

```go
func (fc *FileClient) Restore(ctx context.Context, path string) error
```

Recover a soft-deleted file.

```go
_ = fc.Restore(ctx, "videos/raw/old.mp4")
```

### FileClient.Sync

```go
func (fc *FileClient) Sync(ctx context.Context, path string) error
```

Sync file to R2 edge cache (for frequently-accessed files).

```go
_ = fc.Sync(ctx, "thumbnails/thumb.jpg")
```

Use for:
- Thumbnail images
- Static assets
- Public metadata

## Cache

### NewCacheClient (Production)

```go
func NewCacheClient(client *redis.Client, stage, appID, moduleID string) *CacheClient
```

Create Redis-backed cache client (production).

```go
redisClient := redis.NewClient(&redis.Options{
	Addr: os.Getenv("REDIS_ADDR"),
})

cc := storage.NewCacheClient(redisClient, "prod", appID, moduleID)
```

### NewLocalCacheClient

```go
func NewLocalCacheClient(stage, appID, moduleID string) *CacheClient
```

Create in-memory cache client (local development/testing).

```go
cc := storage.NewLocalCacheClient("dev", appID, moduleID)
```

### CacheClient.Set

```go
func (cc *CacheClient) Set(ctx context.Context, key string, value string, ttl time.Duration) error
```

Set key with TTL.

```go
_ = cc.Set(ctx, "views:v-123", "42", 5*time.Minute)
```

Auto-tracked: `cache_set`

### CacheClient.Get

```go
func (cc *CacheClient) Get(ctx context.Context, key string) (string, error)
```

Get value by key. Returns `storage.ErrCacheMiss` if not found.

```go
val, err := cc.Get(ctx, "views:v-123")
if errors.Is(err, storage.ErrCacheMiss) {
	val = "0"
}
```

Auto-tracked: `cache_get`

### CacheClient.Del

```go
func (cc *CacheClient) Del(ctx context.Context, key string) error
```

Delete key.

```go
_ = cc.Del(ctx, "views:v-123")
```

Auto-tracked: `cache_del`

## Errors

### ErrNotFound

```go
var ErrNotFound = errors.New("not found")
```

Returned by `GetModuleConfig` when config doesn't exist.

### ErrCacheMiss

```go
var ErrCacheMiss = errors.New("cache miss")
```

Returned by `CacheClient.Get` when key not found or expired.

### ErrInvalidPath

```go
var ErrInvalidPath = errors.New("invalid path")
```

Returned when file path fails validation (path traversal, null bytes, etc.).

## Path Prefixing

Paths are automatically prefixed for isolation:

**Files:**
```
applications/{appID}/{moduleID}/{path}
```

**Cache keys:**
```
mirrorstack-{stage}:applications:{appID}:{moduleID}:{key}
```

Module code doesn't need to include prefixes.

## Query Generation (sqlc)

For database access, use [sqlc](https://sqlc.dev/) to generate type-safe queries:

```bash
# Install sqlc
go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest

# Generate queries from sql/
sqlc generate
```

Define queries in `sql/queries.sql`:

```sql
-- name: ListVideos :many
SELECT id, title, duration FROM videos
WHERE app_id = sqlc.arg('app_id')
ORDER BY created_at DESC
LIMIT $1 OFFSET $2;

-- name: GetVideo :one
SELECT * FROM videos WHERE id = $1;

-- name: CreateVideo :exec
INSERT INTO videos (id, app_id, title, duration)
VALUES ($1, $2, $3, $4);
```

Generated usage:

```go
videos, _ := queries.ListVideos(ctx, tx, appID)
video, _ := queries.GetVideo(ctx, tx, videoID)
_ = queries.CreateVideo(ctx, tx, videoID, appID, title, duration)
```

## Pattern: Cache-Aside

```go
const ttl = 1 * time.Hour

// Try cache
val, err := cc.Get(ctx, key)
if err == nil {
	return unmarshal(val)
}

// Load from DB
data, _ := storage.WithSchema(ctx, pool, schema, func(tx pgx.Tx) (T, error) {
	return queries.Get(ctx, tx, id)
})

// Populate cache (non-blocking)
go func() {
	cc.Set(context.Background(), key, marshal(data), ttl)
}()

return data
```

## See Also

- [Storage Guide](../guides/storage.md)
- [Metering Guide](../guides/meter.md)
- [Full Module Example](../examples/full-module.md)
