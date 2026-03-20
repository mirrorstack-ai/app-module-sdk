---
title: ECS Task Example
description: Long-running operations with NewContext and inter-module communication
order: 2
---

## Overview

ECS tasks handle operations that take longer than Lambda's 15-minute timeout. This example shows a video transcoding task that:
- Uses `handler.NewContext()` to access storage and events
- Runs as a separate container with same environment as the module
- Sends progress updates via events
- Auto-tracks metrics for billing

## Launch Task from Module

When a user requests video processing:

```go
// In module handler
func (h *VideoHandler) RequestTranscode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		VideoID string `json:"video_id"`
		Quality string `json:"quality"` // "720p", "1080p", etc.
	}
	handler.DecodeJSON(w, r, &req)

	// Launch ECS task
	result, err := h.eventClient.RunTask(ctx, "transcode", map[string]any{
		"videoId": req.VideoID,
		"quality": req.Quality,
	})
	if err != nil {
		handler.InternalError(w)
		return
	}

	// Return task ID for client polling
	handler.WriteJSON(w, 202, map[string]string{
		"taskId":    result.TaskID,
		"statusUrl": fmt.Sprintf("/tasks/%s/status", result.TaskID),
	})
}
```

## Standalone Task Binary

The ECS task is a separate Go binary in the same module repository:

```
video-module/
├── main.go              # Module handlers
├── transcode/
│   └── main.go          # Transcode task
├── sql/
└── go.mod
```

## Task Entry Point

```go
// transcode/main.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/mirrorstack-ai/app-module-sdk/event"
	"github.com/mirrorstack-ai/app-module-sdk/handler"
	"github.com/mirrorstack-ai/app-module-sdk/meter"
	"github.com/mirrorstack-ai/app-module-sdk/storage"
)

func main() {
	ctx := context.Background()
	handler.InitLogger()

	// Get environment injected by platform
	appID := os.Getenv("APP_ID")
	schemaName := os.Getenv("SCHEMA_NAME")
	moduleID := os.Getenv("MODULE_ID")

	// Build context using injected env vars
	ctx = handler.NewContext(ctx, handler.ContextParams{
		AppID:      appID,
		SchemaName: schemaName,
		ModuleID:   moduleID,
		AuthType:   handler.AuthTypeInternal,
	})

	// Database
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal("database:", err)
	}
	defer pool.Close()

	// Event client
	cfg, _ := awsconfig.LoadDefaultConfig(ctx)
	ec := event.NewClient(
		awslambda.NewFromConfig(cfg),
		os.Getenv("PLATFORM_ARN"),
		moduleID,
	)

	// Cache client
	redisClient := redis.NewClient(&redis.Options{
		Addr: os.Getenv("REDIS_ADDR"),
	})
	cc := storage.NewCacheClient(redisClient, "prod", appID, moduleID)

	// Meter for tracking task metrics
	m := meter.New(&taskSink{ec}, appID, moduleID, 10*time.Second)
	defer m.Close()
	ctx = meter.WithContext(ctx, m)

	// Parse task payload from environment
	var payload struct {
		VideoID string `json:"video_id"`
		Quality string `json:"quality"`
	}
	if err := json.Unmarshal([]byte(os.Getenv("TASK_PAYLOAD")), &payload); err != nil {
		log.Fatal("parse payload:", err)
	}

	// Run transcoding
	handler.Logger(ctx).Info("starting transcode",
		"videoId", payload.VideoID,
		"quality", payload.Quality,
	)

	err = transcodeVideo(ctx, pool, ec, cc, m, payload.VideoID, payload.Quality)
	if err != nil {
		handler.Logger(ctx).Error("transcode failed", "error", err)

		// Emit failure event
		_ = ec.Emit(ctx, "transcode_failed", map[string]any{
			"videoId": payload.VideoID,
			"error":   err.Error(),
		})
		os.Exit(1)
	}

	handler.Logger(ctx).Info("transcode completed", "videoId", payload.VideoID)
}

type taskSink struct {
	ec *event.Client
}

func (s *taskSink) FlushUsage(ctx context.Context, entries []meter.UsageEntry) error {
	return s.ec.Emit(ctx, "usage_tracked", map[string]any{
		"entries": entries,
	})
}
```

## Transcoding Logic

```go
// transcode/transcode.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mirrorstack-ai/app-module-sdk/event"
	"github.com/mirrorstack-ai/app-module-sdk/handler"
	"github.com/mirrorstack-ai/app-module-sdk/meter"
	"github.com/mirrorstack-ai/app-module-sdk/storage"
)

func transcodeVideo(ctx context.Context, pool *pgxpool.Pool, ec *event.Client,
	cc storage.CacheClient, m *meter.Meter, videoID, quality string) error {

	schema := handler.GetSchemaName(ctx)

	// Get video metadata
	video, err := storage.WithSchema(ctx, pool, schema, func(tx pgx.Tx) (*Video, error) {
		return queries.GetVideo(ctx, tx, videoID)
	})
	if err != nil {
		return fmt.Errorf("get video: %w", err)
	}

	// Download original video
	// (In real world, use S3 presigned URL)
	inputPath := fmt.Sprintf("videos/raw/%s.mp4", videoID)
	handler.Logger(ctx).Info("downloading video", "path", inputPath)

	// Emit progress
	_ = ec.Emit(ctx, "transcode_progress", map[string]any{
		"videoId":  videoID,
		"status":   "downloading",
		"progress": 10,
	})

	// Transcode (simulated)
	start := time.Now()
	outputPath, fileSize := ffmpegTranscode(ctx, inputPath, quality)
	duration := time.Since(start)

	// Track transcoding time
	m.Track("transcode_seconds", duration.Seconds())
	m.Track("output_mb", float64(fileSize/1024/1024))

	handler.Logger(ctx).Info("transcode completed",
		"duration", duration,
		"fileSize", fileSize,
	)

	// Emit progress update
	_ = ec.Emit(ctx, "transcode_progress", map[string]any{
		"videoId":  videoID,
		"status":   "uploading",
		"progress": 90,
	})

	// Update video record with output URL
	err = storage.WithSchema(ctx, pool, schema, func(tx pgx.Tx) error {
		return queries.UpdateVideoOutput(ctx, tx, videoID, outputPath, quality)
	})
	if err != nil {
		return fmt.Errorf("update video: %w", err)
	}

	// Invalidate cache
	_ = cc.Del(ctx, fmt.Sprintf("video:%s", videoID))

	// Emit completion event for other modules
	_ = ec.Emit(ctx, "transcode_completed", map[string]any{
		"videoId":   videoID,
		"quality":   quality,
		"url":       outputPath,
		"fileSize":  fileSize,
		"duration":  int(duration.Seconds()),
	})

	return nil
}

// Simulated ffmpeg transcoding
func ffmpegTranscode(ctx context.Context, inputPath, quality string) (string, int64) {
	// Real implementation would:
	// 1. Download from S3
	// 2. Run ffmpeg
	// 3. Upload output to S3
	// 4. Track metrics

	handler.Logger(ctx).Info("transcoding", "input", inputPath, "quality", quality)

	// Simulate work
	time.Sleep(5 * time.Second)

	// Return output path and file size
	return fmt.Sprintf("videos/processed/%s-%s.mp4", inputPath, quality), 50 * 1024 * 1024
}

type Video struct {
	ID       string
	Title    string
	Duration int
}
```

## Progress Polling from Client

Module handler for task status polling:

```go
// In main.go handlers
func (h *VideoHandler) GetTaskStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	taskID := chi.URLParam(r, "taskId")

	// Query ECS for task status
	status, err := h.ecsClient.DescribeTaskStatus(ctx, taskID)
	if err != nil {
		handler.NotFound(w, "task not found")
		return
	}

	handler.WriteJSON(w, 200, map[string]any{
		"taskId":   taskID,
		"status":   status.Status, // PENDING, RUNNING, COMPLETED, FAILED
		"progress": status.Progress,
		"message":  status.Message,
	})
}
```

## Building Task Image

```bash
# Build task binary for Lambda/ECS
cd transcode/
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o task .

# Dockerfile for ECS
FROM public.ecr.aws/lambda/go:latest
COPY transcode/task ${LAMBDA_TASK_ROOT}/
CMD [ "task" ]
```

## Environment Variables

The platform injects these env vars when launching the ECS task:

```bash
APP_ID=app-1
SCHEMA_NAME=app_1
MODULE_ID=video
DATABASE_URL=postgresql://...
REDIS_ADDR=redis:6379
PLATFORM_ARN=arn:aws:lambda:...
TASK_PAYLOAD={"videoId":"v-123","quality":"1080p"}
```

## Error Handling

Task failures are handled via events:

```go
if err != nil {
	// Emit failure event
	ec.Emit(ctx, "transcode_failed", map[string]any{
		"videoId": videoID,
		"error":   err.Error(),
	})
	os.Exit(1)
}
```

Module handler can subscribe:

```go
event.Register(r, map[string]event.HandlerFunc{
	"video.transcode_failed": h.OnTranscodeFailed,
})

func (h *Handler) OnTranscodeFailed(w http.ResponseWriter, r *http.Request, evt event.Event) {
	var payload struct {
		VideoID string `json:"video_id"`
		Error   string `json:"error"`
	}
	json.Unmarshal(evt.Payload, &payload)

	// Update video status, notify user, etc.
	h.service.MarkVideoFailed(r.Context(), payload.VideoID, payload.Error)
	handler.WriteJSON(w, 200, map[string]bool{"ok": true})
}
```

## Timeout Handling

Tasks have different timeout limits:
- Lambda: 15 minutes
- ECS: 24 hours (configurable)

For operations > 15 minutes, always use `RunTask()`:

```go
if estimatedTime > 15*time.Minute {
	// Use ECS task
	result, _ := ec.RunTask(ctx, "long_operation", payload)
} else {
	// Can use Lambda with SSE streaming
	// or quick ECS task
}
```

## See Also

- [Event Guide](../guides/event.md)
- [Storage Guide](../guides/storage.md)
- [Metering Guide](../guides/meter.md)
- [Full Module Example](./full-module.md)
