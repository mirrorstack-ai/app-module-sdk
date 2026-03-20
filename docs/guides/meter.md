---
title: Usage Metering & Billing
description: Auto-tracking metrics, cost allocation, custom measurements
order: 4
---

## Overview

The Meter collects usage metrics for billing and cost allocation. Most operations are auto-tracked; you only add custom metrics when needed.

## Setup

### Create Meter

```go
import "github.com/mirrorstack-ai/app-module-sdk/meter"

m := meter.New(sink, appID, moduleID, 30*time.Second)
defer m.Close()
```

Parameters:
- `sink` — Implementation that sends metrics to platform
- `appID` — Application ID
- `moduleID` — This module's ID
- `interval` — Auto-flush period (set 0 to disable auto-flush)

### Implement UsageSink

```go
type platformUsageSink struct {
	ec *event.Client
}

func (s *platformUsageSink) FlushUsage(ctx context.Context, entries []meter.UsageEntry) error {
	// Send to platform for aggregation and billing
	return s.ec.Emit(ctx, "usage_tracked", map[string]any{
		"entries": entries,
	})
}
```

### Inject into Handler Context

```go
func meterMiddleware(m *meter.Meter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := meter.WithContext(r.Context(), m)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

r := chi.NewRouter()
r.Use(handler.ExtractContext)
r.Use(handler.RequestLogger)
r.Use(meterMiddleware(m))
```

## Auto-Tracked Metrics

These metrics are automatically tracked when a meter is in the context:

| Operation | Metric | Unit |
|-----------|--------|------|
| `storage.WithSchema()` | `db_duration_ms` | milliseconds |
| `storage.WithSchema()` | `db_queries` | count |
| `CacheClient.Get()` | `cache_get` | count |
| `CacheClient.Set()` | `cache_set` | count |
| `CacheClient.Del()` | `cache_del` | count |
| `FileClient.PresignPut()` | `file_upload` | count |
| `FileClient.PresignGet()` | `file_download` | count |
| `FileClient.Delete()` | `file_delete` | count |

## Custom Metrics

Track module-specific operations:

```go
m.Track("transcode_minutes", 12.5)
m.Track("ai_tokens", 15000)
m.Track("email_sent", 1)
```

### Naming Convention

Use descriptive, singular names:
- `transcode_minutes` — Time spent transcoding
- `ai_tokens` — LLM tokens consumed
- `email_sent` — Count of emails
- `storage_gb` — Storage used

### Common Patterns

```go
// Count API calls
m.Track("api_calls", 1)

// Duration in seconds
duration := time.Since(start).Seconds()
m.Track("processing_seconds", duration)

// File size in MB
m.Track("upload_mb", float64(fileSize) / 1024 / 1024)

// Resource count
m.Track("videos_transcoded", int(count))
```

## Flushing

### Auto-Flush

Meter auto-flushes every `interval` (30 seconds by default):

```go
m := meter.New(sink, appID, moduleID, 30*time.Second)
// Entries are auto-flushed every 30 seconds
```

### Manual Flush

```go
err := m.Flush(ctx)
```

### Graceful Shutdown

Always defer `m.Close()` to flush remaining entries:

```go
m := meter.New(sink, appID, moduleID, 30*time.Second)
defer m.Close()  // Flushes on shutdown
```

## UsageEntry Structure

```go
type UsageEntry struct {
	AppID    string  `json:"app_id"`
	ModuleID string  `json:"module_id"`
	Metric   string  `json:"metric"`
	Value    float64 `json:"value"`
	Time     int64   `json:"time"` // unix ms
}
```

## Billing Integration

Usage entries flow:
1. Module's meter tracks entries
2. Meter flushes to platform (via event)
3. Platform aggregates metrics
4. Billing engine uses aggregated metrics
5. Cost allocated per app/module

### Cost Allocation

The platform:
- Joins meter metrics with AWS Cost Explorer data
- Allocates infrastructure costs proportionally
- Generates cost reports per app/module
- Enforces budget alerts (future)

## ECS Task Metering

For long-running ECS tasks, create a meter in the task:

```go
func main() {
	// Task is launched with env vars
	ctx := handler.NewContext(context.Background(), handler.ContextParams{
		AppID:      os.Getenv("APP_ID"),
		SchemaName: os.Getenv("SCHEMA_NAME"),
		ModuleID:   os.Getenv("MODULE_ID"),
		AuthType:   handler.AuthTypeInternal,
	})

	// Create meter for this task
	sink := &platformUsageSink{
		ec: eventClient,
	}
	m := meter.New(sink, appID, moduleID, 10*time.Second)
	defer m.Close()

	// Inject meter into operations
	ctx = meter.WithContext(ctx, m)

	// Now storage operations auto-track
	result := storage.WithSchema(ctx, pool, schema, func(tx pgx.Tx) error {
		return queries.ProcessVideo(ctx, tx, videoID)
	})

	// Custom tracking
	m.Track("transcode_minutes", duration.Minutes())
}
```

## Examples

### Video Transcoding

```go
func (h *Handler) TranscodeVideo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := meter.FromContext(ctx)

	start := time.Now()

	// Process video
	video, err := h.transcode(ctx, videoID)
	if err != nil {
		handler.InternalError(w)
		return
	}

	// Track custom metrics
	duration := time.Since(start).Seconds()
	m.Track("transcode_seconds", duration)
	m.Track("output_mb", float64(video.FileSize) / 1024 / 1024)
	m.Track("bitrate_kbps", float64(video.Bitrate) / 1024)

	// DB write auto-tracks db_duration_ms, db_queries
	err = storage.WithSchema(ctx, h.pool, handler.GetSchemaName(ctx),
		func(tx pgx.Tx) error {
			return queries.UpdateVideo(ctx, tx, video)
		})

	handler.WriteJSON(w, 200, video)
}
```

### AI-Powered Feature

```go
func (h *Handler) GenerateCaption(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := meter.FromContext(ctx)

	// Call AI service
	caption, inputTokens, outputTokens := h.ai.Generate(ctx, text)

	// Track token usage
	m.Track("ai_input_tokens", float64(inputTokens))
	m.Track("ai_output_tokens", float64(outputTokens))

	// Total tokens for billing
	total := float64(inputTokens + outputTokens)
	m.Track("ai_tokens_total", total)

	handler.WriteJSON(w, 200, map[string]string{"caption": caption})
}
```

### Cache Operations

```go
func (h *Handler) GetUserStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// Cache client auto-tracks cache_get/cache_set/cache_del

	stats, err := h.cc.Get(ctx, fmt.Sprintf("stats:%s", userID))
	if err == nil {
		// Cache hit — auto-tracked as cache_get
		return stats
	}

	// Cache miss — load from DB
	// Auto-tracked: db_duration_ms, db_queries
	stats = storage.WithSchema(ctx, h.pool, schema, func(tx pgx.Tx) (*Stats, error) {
		return queries.GetStats(ctx, tx, userID)
	})

	// Populate cache — auto-tracked as cache_set
	h.cc.Set(ctx, fmt.Sprintf("stats:%s", userID), stats, 1*time.Hour)

	return stats
}
```

## Best Practices

1. **Always defer `m.Close()`** — Ensures final flush on shutdown
2. **Use consistent metric names** — Easier aggregation and billing
3. **Track at the right granularity** — Per-operation, not per-internal-loop
4. **No high-cardinality dimensions** — Avoid embedding IDs in metric names
5. **Test metric collection** — Use local cache client to verify tracking

## Troubleshooting

### Metrics not appearing

- Ensure meter is in context via middleware
- Check that `Flush()` or `Close()` is called
- Verify sink implementation sends to platform
- Check logs for flush errors

### Missing auto-tracked metrics

- Ensure meter is in context when calling storage operations
- Auto-tracking only works when meter is present
- Manual tracking via `m.Track()` works regardless

## See Also

- [meter Package Reference](../api/meter.md)
- [Storage Guide](./storage.md)
- [Full Module Example](../examples/full-module.md)
