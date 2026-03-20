---
title: MirrorStack Module SDK
description: Go SDK for building self-contained feature modules on MirrorStack
order: 0
---

## Overview

The Module SDK provides everything you need to build MirrorStack modules—self-contained feature units that run as standalone Lambda functions behind the MirrorStack Core API proxy.

## Key Features

- **HTTP Lifecycle** — Context extraction, auth, JSON request/response, pagination, logging, SSE
- **Inter-Module Communication** — Event pub/sub, synchronous calls, ECS task orchestration
- **Data Access** — Multi-tenant database isolation, file storage (S3/local), Redis caching
- **Usage Metering** — Auto-tracking of storage operations, custom metrics for billing
- **Auto Detection** — Runs on Lambda or local HTTP server automatically

## Navigation

### Getting Started
- [Install & Quick Start](./getting-started.md)
- [Module Lifecycle](./getting-started.md#module-lifecycle)

### Guides
- [HTTP Handlers & Context](./guides/handler.md)
- [Inter-Module Events](./guides/event.md)
- [Database, File & Cache Storage](./guides/storage.md)
- [Usage Metering & Billing](./guides/meter.md)

### API References
- [handler Package](./api/handler.md)
- [event Package](./api/event.md)
- [storage Package](./api/storage.md)
- [meter Package](./api/meter.md)

### Examples & Security
- [Complete Module Example](./examples/full-module.md)
- [ECS Task with NewContext](./examples/ecs-task.md)
- [Security Features](./security.md)

## Quick Start

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
	r.Get("/videos", listVideos)
	module.Start(r) // Auto-detects Lambda vs HTTP
}
```

## Packages at a Glance

| Package | Purpose |
|---------|---------|
| `bootstrap` | Root package — Start, HealthCheck, graceful shutdown |
| `handler` | HTTP lifecycle, context, auth, JSON responses |
| `event` | Inter-module communication (Emit, Call, RunTask) |
| `storage` | Database, file storage, cache access |
| `meter` | Usage metering and cost tracking |

## Architecture

Modules integrate with MirrorStack Core API which:
- Injects request context via X-MS-* headers
- Routes requests to module Lambda functions
- Mediates inter-module communication
- Tracks usage metrics for billing

## Requirements

- Go 1.24+
- AWS SDK for Go v2
- chi router (or any http.Handler compatible router)
- PostgreSQL (for multi-tenant database)
- AWS Lambda (or local HTTP dev mode)

## Next Steps

1. [Install the SDK](./getting-started.md)
2. [Learn the handler lifecycle](./guides/handler.md)
3. [Explore event communication](./guides/event.md)
4. [Review a complete example](./examples/full-module.md)
5. [Understand security](./security.md)
