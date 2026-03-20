# MirrorStack Module SDK

Go SDK for building MirrorStack modules — self-contained feature units that run as standalone Lambda functions behind the Core API proxy.

## Install

```bash
go get github.com/mirrorstack-ai/app-module-sdk
```

Requires Go 1.24+.

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

    module.Start(r)
}
```

## Packages

| Package | Import | Purpose |
|---------|--------|---------|
| `module` | `.../module` | Bootstrap, health check, manifest types, graceful shutdown |
| `handler` | `.../handler` | Context, auth, JSON, pagination, logging, SSE |
| `event` | `.../event` | Inter-module events, sync calls, ECS tasks |
| `storage` | `.../storage` | Database, S3/R2 file storage, Redis cache |
| `meter` | `.../meter` | Usage metering and cost allocation |

## Documentation

Full docs at [docs.mirrorstack.ai](https://docs.mirrorstack.ai) or browse the [docs/](./docs/) directory.

## Testing

```bash
go test -race ./...   # 131 tests
```
