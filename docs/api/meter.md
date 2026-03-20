---
title: meter Package API Reference
description: Usage tracking, metrics, and billing integration
order: 4
---

## Meter Lifecycle

### New

```go
func New(sink UsageSink, appID, moduleID string, interval time.Duration) *Meter
```

Create a meter that flushes to sink periodically.

```go
m := meter.New(sink, appID, moduleID, 30*time.Second)
defer m.Close()
```

Parameters:
- `sink` — Receives batched usage entries
- `appID` — Application identifier
- `moduleID` — This module's identifier
- `interval` — Auto-flush period (0 to disable)

### Close

```go
func (m *Meter) Close() error
```

Stop auto-flush and flush remaining entries.

```go
m := meter.New(sink, appID, moduleID, 30*time.Second)
defer m.Close()  // Always call in defer
```

## Context Integration

### WithContext

```go
func WithContext(ctx context.Context, m *Meter) context.Context
```

Add meter to context. Storage operations auto-track metrics when meter is present.

```go
ctx = meter.WithContext(r.Context(), m)
```

### FromContext

```go
func FromContext(ctx context.Context) *Meter
```

Retrieve meter from context. Returns nil if not set.

```go
m := meter.FromContext(ctx)
if m != nil {
	m.Track("api_call", 1)
}
```

## Metric Tracking

### Track

```go
func (m *Meter) Track(metric string, value float64)
```

Record a usage metric (non-blocking).

```go
m.Track("transcode_minutes", 12.5)
m.Track("api_calls", 1)
m.Track("errors", 1)
```

Accepts:
- Integers: `m.Track("count", 42)`
- Floats: `m.Track("duration_seconds", 3.14)`
- Negative values: `m.Track("refund", -10)`

### Flush

```go
func (m *Meter) Flush(ctx context.Context) error
```

Send buffered entries to sink immediately.

```go
if err := m.Flush(ctx); err != nil {
	log.Printf("flush failed: %v", err)
}
```

Automatically called:
- Every `interval` (auto-flush)
- On `Close()`

## UsageSink

### UsageSink Interface

```go
type UsageSink interface {
	FlushUsage(ctx context.Context, entries []UsageEntry) error
}
```

Implement to handle metrics (typically sends to platform).

```go
type platformSink struct {
	eventClient *event.Client
}

func (s *platformSink) FlushUsage(ctx context.Context, entries []meter.UsageEntry) error {
	return s.eventClient.Emit(ctx, "usage_tracked", map[string]any{
		"entries": entries,
	})
}
```

### UsageEntry

```go
type UsageEntry struct {
	AppID    string  // Application ID
	ModuleID string  // Module ID
	Metric   string  // Metric name
	Value    float64 // Numeric value
	Time     int64   // Unix milliseconds
}
```

## Auto-Tracked Metrics

When meter is in context, these are automatically tracked:

| Operation | Metric | Value | Source |
|-----------|--------|-------|--------|
| `storage.WithSchema()` | `db_duration_ms` | milliseconds | db_duration_ms |
| `storage.WithSchema()` | `db_queries` | count | db_queries |
| `CacheClient.Get()` | `cache_get` | 1 | cache_get |
| `CacheClient.Set()` | `cache_set` | 1 | cache_set |
| `CacheClient.Del()` | `cache_del` | 1 | cache_del |
| `FileClient.PresignPut()` | `file_upload` | 1 | file_upload |
| `FileClient.PresignGet()` | `file_download` | 1 | file_download |
| `FileClient.Delete()` | `file_delete` | 1 | file_delete |

## Middleware Pattern

### Setup

```go
func meterMiddleware(m *meter.Meter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := meter.WithContext(r.Context(), m)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
```

### Registration

```go
m := meter.New(sink, appID, moduleID, 30*time.Second)
defer m.Close()

r := chi.NewRouter()
r.Use(handler.ExtractContext)
r.Use(meterMiddleware(m))  // After ExtractContext
```

## Usage Examples

### Basic Tracking

```go
// Count operations
m.Track("api_call", 1)
m.Track("login", 1)

// Duration
start := time.Now()
// ... do work ...
duration := time.Since(start).Seconds()
m.Track("processing_seconds", duration)

// Size
m.Track("upload_mb", float64(fileSize / 1024 / 1024))
```

### Handler Integration

```go
func (h *Handler) ProcessVideo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := meter.FromContext(ctx)

	start := time.Now()

	// Storage access auto-tracks: db_duration_ms, db_queries
	videos, _ := storage.WithSchema(ctx, h.pool, schema, func(tx pgx.Tx) ([]Video, error) {
		return queries.ListVideos(ctx, tx)
	})

	// Cache access auto-tracks: cache_get, cache_set
	h.cc.Set(ctx, "cached_list", marshal(videos), 1*time.Hour)

	// Manual tracking
	duration := time.Since(start).Seconds()
	m.Track("request_seconds", duration)

	handler.WriteJSON(w, 200, videos)
}
```

### Custom Metrics

```go
func (h *Handler) TranscodeVideo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := meter.FromContext(ctx)

	result := h.transcode(ctx, videoID)

	// Track custom metrics
	m.Track("transcode_seconds", result.Duration.Seconds())
	m.Track("output_mb", float64(result.FileSize / 1024 / 1024))
	m.Track("bitrate_kbps", float64(result.Bitrate / 1024))

	handler.WriteJSON(w, 200, result)
}
```

### Conditional Tracking

```go
if m := meter.FromContext(ctx); m != nil {
	m.Track("rare_operation", 1)
}
```

## ECS Task Metering

Create meter in task runtime:

```go
func main() {
	ctx := handler.NewContext(context.Background(), handler.ContextParams{
		AppID:      os.Getenv("APP_ID"),
		SchemaName: os.Getenv("SCHEMA_NAME"),
		ModuleID:   os.Getenv("MODULE_ID"),
		AuthType:   handler.AuthTypeInternal,
	})

	sink := &platformSink{ec: eventClient}
	m := meter.New(sink, appID, moduleID, 10*time.Second)
	defer m.Close()

	ctx = meter.WithContext(ctx, m)

	// Storage and event operations auto-track
	processVideo(ctx)
}
```

## Metric Naming

Best practices:

- **Use descriptive names**: `transcode_minutes` not `tx_mins`
- **Use singular**: `video_processed` not `videos_processed`
- **No high cardinality**: Don't include IDs in metric names
- **Group related**: `cache_hits`, `cache_misses`, `cache_evictions`
- **Use standard units**: `_seconds`, `_mb`, `_count`

## Flush Behavior

### Auto-Flush

Meter auto-flushes every `interval`:

```go
m := meter.New(sink, appID, moduleID, 30*time.Second)
// Entries automatically flush every 30 seconds
```

### Manual Flush

```go
err := m.Flush(ctx)
```

### Graceful Shutdown

Always defer `Close()`:

```go
m := meter.New(sink, appID, moduleID, 30*time.Second)
defer m.Close()  // Flushes remaining entries
```

## Thread Safety

Meter is safe for concurrent use. Multiple goroutines can call `Track()` simultaneously.

```go
m.Track("metric", 1)  // Safe from multiple goroutines
```

## See Also

- [Metering Guide](../guides/meter.md)
- [Storage Guide](../guides/storage.md)
- [Full Module Example](../examples/full-module.md)
