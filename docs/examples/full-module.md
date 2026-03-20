---
title: Complete Module Example
description: Full working video module with handlers, events, storage, and metering
order: 1
---

## Overview

A complete video module example showing:
- HTTP handlers with auth
- Database access with schema isolation
- Inter-module communication (events)
- File storage (presigned URLs)
- Caching patterns
- Usage metering

## Project Structure

```
video-module/
├── main.go              # Entry point and routing
├── handlers.go          # HTTP request handlers
├── service.go           # Business logic
├── models.go            # Data structures
├── sql/
│   ├── schema.sql       # Table definitions
│   └── queries.sql      # sqlc queries
├── go.mod
└── go.sum
```

## Main Entry Point

```go
// main.go
package main

import (
	"context"
	"os"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/mirrorstack-ai/app-module-sdk/module"
	"github.com/mirrorstack-ai/app-module-sdk/event"
	"github.com/mirrorstack-ai/app-module-sdk/handler"
	"github.com/mirrorstack-ai/app-module-sdk/meter"
	"github.com/mirrorstack-ai/app-module-sdk/storage"
)

func main() {
	ctx := context.Background()
	handler.InitLogger()

	// Database connection pool
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		panic(err)
	}
	defer pool.Close()

	// Event client
	var ec *event.Client
	if os.Getenv("AWS_LAMBDA_FUNCTION_NAME") != "" {
		cfg, _ := awsconfig.LoadDefaultConfig(ctx)
		ec = event.NewClient(
			awslambda.NewFromConfig(cfg),
			os.Getenv("PLATFORM_ARN"),
			"video",
		)
	} else {
		ec = event.NewHTTPClient("http://localhost:3000", "dev-secret", "video")
	}

	// File client
	var fc storage.FileClient
	if os.Getenv("AWS_LAMBDA_FUNCTION_NAME") != "" {
		cfg, _ := awsconfig.LoadDefaultConfig(ctx)
		s3Client := awss3.NewFromConfig(cfg)
		fc = storage.NewFileClient(
			awss3.NewPresignFromClient(s3Client),
			s3Client,
			nil, // edgeNotifier
			os.Getenv("S3_BUCKET"),
			os.Getenv("APP_ID"),
			"video",
		)
	} else {
		fc = storage.NewLocalFileClient(
			"/tmp/mirrorstack",
			"http://localhost:9000",
			os.Getenv("APP_ID"),
			"video",
		)
	}

	// Cache client
	var cc storage.CacheClient
	if os.Getenv("AWS_LAMBDA_FUNCTION_NAME") != "" {
		redisClient := redis.NewClient(&redis.Options{
			Addr: os.Getenv("REDIS_ADDR"),
		})
		cc = storage.NewCacheClient(redisClient, "prod", os.Getenv("APP_ID"), "video")
	} else {
		cc = storage.NewLocalCacheClient("dev", os.Getenv("APP_ID"), "video")
	}

	// Meter for usage tracking
	m := meter.New(&platformSink{ec}, os.Getenv("APP_ID"), "video", 30*time.Second)
	defer m.Close()

	// Service layer
	appID := os.Getenv("APP_ID")
	svc := &VideoService{
		pool: pool,
		ec:   ec,
		fc:   fc,
		cc:   cc,
	}

	h := &VideoHandler{
		service: svc,
		appID:   appID,
	}

	// Router setup
	r := chi.NewRouter()
	r.Use(handler.ExtractContext)
	r.Use(handler.RequestLogger)
	r.Use(meterMiddleware(m))

	// Public routes
	r.Get("/videos", h.ListVideos)
	r.Get("/videos/{id}", h.GetVideo)
	r.Post("/videos/{id}/view", h.RecordView)

	// Admin routes
	r.Route("/admin", func(r chi.Router) {
		r.Use(handler.RequirePlatformUser())
		r.Post("/videos", h.CreateVideo)
		r.Put("/videos/{id}", h.UpdateVideo)
		r.Delete("/videos/{id}", h.DeleteVideo)
		r.Post("/videos/{id}/unlock", h.UnlockVideo)
	})

	// Event subscribers
	event.Register(r, map[string]event.HandlerFunc{
		"oauth.user_deleted": h.OnUserDeleted,
	})

	module.Start(r)
}

func meterMiddleware(m *meter.Meter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := meter.WithContext(r.Context(), m)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

type platformSink struct {
	ec *event.Client
}

func (s *platformSink) FlushUsage(ctx context.Context, entries []meter.UsageEntry) error {
	return s.ec.Emit(ctx, "usage_tracked", map[string]any{
		"entries": entries,
	})
}
```

## Models

```go
// models.go
package main

import "time"

type Video struct {
	ID           string    `json:"id"`
	AppID        string    `json:"app_id"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	Duration     int       `json:"duration"`
	ThumbnailURL string    `json:"thumbnail_url"`
	CreditCost   int       `json:"credit_cost"`
	Unlocked     bool      `json:"unlocked"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type CreateVideoRequest struct {
	Title        string `json:"title"`
	Description  string `json:"description"`
	Duration     int    `json:"duration"`
	CreditCost   int    `json:"credit_cost"`
}

type GetUploadURLResponse struct {
	VideoID   string `json:"video_id"`
	UploadURL string `json:"upload_url"`
}
```

## Service Layer

```go
// service.go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mirrorstack-ai/app-module-sdk/event"
	"github.com/mirrorstack-ai/app-module-sdk/storage"
)

type VideoService struct {
	pool storage.FileClient
	ec   *event.Client
	fc   storage.FileClient
	cc   storage.CacheClient
}

func (s *VideoService) ListVideos(ctx context.Context, schema string, limit, offset int) ([]Video, int, error) {
	videos, total, err := storage.WithSchema(ctx, s.pool, schema, func(tx pgx.Tx) ([]Video, int, error) {
		// This would use sqlc-generated queries
		return queries.ListVideos(ctx, tx, limit, offset)
	})
	return videos, total, err
}

func (s *VideoService) GetVideo(ctx context.Context, schema, videoID string) (*Video, error) {
	// Try cache first
	key := fmt.Sprintf("video:%s", videoID)
	cached, err := s.cc.Get(ctx, key)
	if err == nil {
		return unmarshalVideo(cached)
	}

	// Load from DB
	video, err := storage.WithSchema(ctx, s.pool, schema, func(tx pgx.Tx) (*Video, error) {
		return queries.GetVideo(ctx, tx, videoID)
	})
	if err != nil {
		return nil, err
	}

	// Populate cache
	go func() {
		s.cc.Set(context.Background(), key, marshalVideo(video), 1*time.Hour)
	}()

	return video, nil
}

func (s *VideoService) CreateVideo(ctx context.Context, schema, appID string, req CreateVideoRequest) (*Video, error) {
	video := &Video{
		ID:          uuid.New().String(),
		AppID:       appID,
		Title:       req.Title,
		Description: req.Description,
		Duration:    req.Duration,
		CreditCost:  req.CreditCost,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	err := storage.WithSchema(ctx, s.pool, schema, func(tx pgx.Tx) error {
		return queries.CreateVideo(ctx, tx, video)
	})
	if err != nil {
		return nil, err
	}

	// Emit event for other modules
	_ = s.ec.Emit(ctx, "video_created", map[string]any{
		"videoId": video.ID,
		"title":   video.Title,
	})

	return video, nil
}

func (s *VideoService) UnlockVideo(ctx context.Context, schema, userID, videoID string) (int, error) {
	// Get video
	video, err := s.GetVideo(ctx, schema, videoID)
	if err != nil {
		return 0, err
	}

	// Call credit module
	var resp struct {
		NewBalance int `json:"new_balance"`
	}
	err = s.ec.Call(ctx, "credit", "v1", "/deduct", map[string]any{
		"userId": userID,
		"amount": video.CreditCost,
	}, &resp)
	if err != nil {
		return 0, err
	}

	// Record unlock
	err = storage.WithSchema(ctx, s.pool, schema, func(tx pgx.Tx) error {
		return queries.RecordUnlock(ctx, tx, userID, videoID)
	})
	if err != nil {
		return 0, err
	}

	// Invalidate cache
	_ = s.cc.Del(ctx, fmt.Sprintf("video:%s", videoID))

	return resp.NewBalance, nil
}

func (s *VideoService) OnUserDeleted(ctx context.Context, schema, userID string) error {
	// Clean up user-specific data
	return storage.WithSchema(ctx, s.pool, schema, func(tx pgx.Tx) error {
		return queries.DeleteUserData(ctx, tx, userID)
	})
}
```

## Handlers

```go
// handlers.go
package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mirrorstack-ai/app-module-sdk/handler"
)

type VideoHandler struct {
	service *VideoService
	appID   string
}

func (h *VideoHandler) ListVideos(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := handler.ParsePagination(r)

	schema := handler.GetSchemaName(ctx)
	videos, total, err := h.service.ListVideos(ctx, schema, p.Limit, p.Offset)
	if err != nil {
		handler.InternalError(w)
		return
	}

	handler.WriteJSON(w, 200, handler.NewPaginatedResponse(videos, total, p))
}

func (h *VideoHandler) GetVideo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	videoID := chi.URLParam(r, "id")

	schema := handler.GetSchemaName(ctx)
	video, err := h.service.GetVideo(ctx, schema, videoID)
	if err != nil {
		handler.NotFound(w, "video not found")
		return
	}

	handler.WriteJSON(w, 200, video)
}

func (h *VideoHandler) CreateVideo(w http.ResponseWriter, r *http.Request) {
	var req CreateVideoRequest
	if err := handler.DecodeJSON(w, r, &req); err != nil {
		return
	}

	if req.Title == "" {
		handler.BadRequest(w, "title is required")
		return
	}

	ctx := r.Context()
	schema := handler.GetSchemaName(ctx)

	video, err := h.service.CreateVideo(ctx, schema, h.appID, req)
	if err != nil {
		handler.InternalError(w)
		return
	}

	handler.WriteJSON(w, 201, video)
}

func (h *VideoHandler) UnlockVideo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := handler.GetPlatformUserID(ctx)
	videoID := chi.URLParam(r, "id")
	schema := handler.GetSchemaName(ctx)

	newBalance, err := h.service.UnlockVideo(ctx, schema, userID, videoID)
	if err != nil {
		handler.InternalError(w)
		return
	}

	handler.WriteJSON(w, 200, map[string]int{
		"newBalance": newBalance,
	})
}

func (h *VideoHandler) RecordView(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	videoID := chi.URLParam(r, "id")
	userID := handler.GetPlatformUserID(ctx)
	schema := handler.GetSchemaName(ctx)
	m := meter.FromContext(ctx)

	// Record view in DB
	err := h.service.RecordView(ctx, schema, userID, videoID)
	if err != nil {
		handler.InternalError(w)
		return
	}

	// Track custom metric
	m.Track("video_view", 1)

	handler.WriteJSON(w, 200, map[string]bool{"ok": true})
}

func (h *VideoHandler) OnUserDeleted(w http.ResponseWriter, r *http.Request, evt event.Event) {
	var payload struct {
		UserID string `json:"user_id"`
	}
	_ = json.Unmarshal(evt.Payload, &payload)

	schema := handler.GetSchemaName(r.Context())
	_ = h.service.OnUserDeleted(r.Context(), schema, payload.UserID)

	handler.WriteJSON(w, 200, map[string]bool{"ok": true})
}
```

## SQL Queries (sqlc)

```sql
-- sql/queries.sql
-- name: ListVideos :many
SELECT id, app_id, title, description, duration, thumbnail_url, credit_cost, unlocked, created_at, updated_at
FROM videos
WHERE app_id = sqlc.arg('app_id')
ORDER BY created_at DESC
LIMIT $1 OFFSET $2;

-- name: GetVideo :one
SELECT * FROM videos WHERE id = $1 AND app_id = sqlc.arg('app_id');

-- name: CreateVideo :exec
INSERT INTO videos (id, app_id, title, description, duration, credit_cost, created_at, updated_at)
VALUES ($1, sqlc.arg('app_id'), $3, $4, $5, $6, NOW(), NOW());

-- name: RecordUnlock :exec
INSERT INTO unlocks (user_id, video_id, unlocked_at) VALUES ($1, $2, NOW())
ON CONFLICT DO NOTHING;

-- name: RecordView :exec
INSERT INTO views (user_id, video_id, viewed_at) VALUES ($1, $2, NOW());

-- name: DeleteUserData :exec
DELETE FROM views WHERE user_id = $1;
DELETE FROM unlocks WHERE user_id = $1;
```

## Building & Running

```bash
# Generate sqlc queries
sqlc generate

# Build for Lambda
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o bootstrap .

# Local development
PORT=8080 DATABASE_URL="postgresql://..." go run .
```

## See Also

- [Handler Guide](../guides/handler.md)
- [Event Guide](../guides/event.md)
- [Storage Guide](../guides/storage.md)
- [Metering Guide](../guides/meter.md)
