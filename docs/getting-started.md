---
title: Getting Started
description: Install SDK, quick start, and understand module lifecycle
order: 1
---

## Installation

```bash
go get github.com/mirrorstack-ai/app-module-sdk
```

## Minimal Example

```go
package main

import (
	"github.com/mirrorstack-ai/app-module-sdk/module"
	"github.com/mirrorstack-ai/app-module-sdk/handler"
	"github.com/go-chi/chi/v5"
)

func main() {
	handler.InitLogger()

	r := chi.NewRouter()
	r.Use(handler.ExtractContext)
	r.Use(handler.RequestLogger)

	r.Get("/hello", func(w http.ResponseWriter, r *http.Request) {
		handler.WriteJSON(w, 200, map[string]string{"message": "hello"})
	})

	module.Start(r) // Auto-detects Lambda vs HTTP
}
```

## Module Lifecycle

### Development (Local HTTP)

- Run module as HTTP server on `:8080`
- Event client uses HTTP fallback
- File storage uses local filesystem
- Cache uses local Redis

### Production (Lambda)

- Module runs as Lambda function
- Core API proxies HTTP requests
- Inter-module communication via Lambda invoke
- File storage uses S3
- Cache uses ElastiCache

### Auto-Detection

`module.Start()` automatically detects the runtime:
- If `AWS_LAMBDA_FUNCTION_NAME` env var is set → Lambda mode
- Otherwise → HTTP server mode (default port 8080, override with `PORT`)

## Project Structure

```
my-module/
├── main.go              # Entry point, routing
├── handler.go           # HTTP request handlers
├── service.go           # Business logic
├── sql/
│   ├── queries.sql      # sqlc SQL queries
│   └── schema.sql       # Migration/schema
├── go.mod
└── go.sum
```

## Core Concepts

### Request Context

Every HTTP request carries context via X-MS-* headers:
- `X-MS-App-ID` — Application identifier
- `X-MS-Schema-Name` — Tenant database schema
- `X-MS-Module-ID` — This module's ID
- `X-MS-Request-ID` — Request correlation ID
- `X-MS-Platform-User-ID` — Authenticated user

Use `handler.ExtractContext` middleware to populate context automatically.

### Handler Pattern

```go
type Handler struct {
	pool *pgxpool.Pool
	ec   *event.Client
}

func (h *Handler) GetVideo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	videoID := chi.URLParam(r, "id")

	video, err := storage.WithSchema(ctx, h.pool, handler.GetSchemaName(ctx),
		func(tx pgx.Tx) (*Video, error) {
			return queries.GetVideo(ctx, tx, videoID)
		})
	if err != nil {
		handler.NotFound(w, "video not found")
		return
	}

	handler.WriteJSON(w, 200, video)
}
```

### Middleware Stack

```go
r := chi.NewRouter()
r.Use(handler.ExtractContext)      // 1. Extract X-MS-* headers
r.Use(handler.RequestLogger)       // 2. Structured logging
r.Use(authMiddleware)              // 3. Your auth
r.Use(meterMiddleware)             // 4. Usage tracking
```

## Environment Variables

### Required

| Variable | Purpose |
|----------|---------|
| `DATABASE_URL` | PostgreSQL connection string |
| `PLATFORM_ARN` | Lambda ARN for Core API (prod only) |

### Optional

| Variable | Default | Purpose |
|----------|---------|---------|
| `PORT` | 8080 | HTTP server port (dev only) |
| `LOG_LEVEL` | info | Log level (debug/info/warn/error) |
| `AWS_REGION` | us-east-1 | AWS region |

## Building & Deploying

### Build for Lambda

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o bootstrap .
```

### Deploy with Infra

```bash
npx cdk deploy
```

## Next Steps

- [Learn HTTP Handlers](./guides/handler.md)
- [Master Inter-Module Events](./guides/event.md)
- [Explore Storage Options](./guides/storage.md)
- [Track Usage with Metering](./guides/meter.md)
