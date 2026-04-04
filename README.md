# @mirrorstack-ai/app-module-sdk

[![License: FSL-1.1-ALv2](https://img.shields.io/badge/License-FSL--1.1--ALv2-blue.svg)](LICENSE)
[![Good First Issues](https://img.shields.io/github/issues/mirrorstack-ai/app-module-sdk/good%20first%20issue?color=7057ff&label=good%20first%20issues)](https://github.com/mirrorstack-ai/app-module-sdk/issues?q=is%3Aopen+label%3A%22good+first+issue%22)

Go SDK for building modules on [MirrorStack](https://mirrorstack.ai), the Agentic CMS platform.

Built with Go, chi router, and designed for AWS Lambda + local dev.

**[Issues](https://github.com/mirrorstack-ai/app-module-sdk/issues)** | **[Good First Issues](https://github.com/mirrorstack-ai/app-module-sdk/issues?q=is%3Aopen+label%3A%22good+first+issue%22)** | **[Slack](https://join.slack.com/t/mirrorstackai/shared_invite/zt-3twmj15cm-EPfQscE71I~JJj0yHK6EZg)**

> **Status:** Under active development. Core is implemented — see the roadmap below.
>
> The `reference/v2-restored` branch contains the original implementation (handler, event, meter, storage packages with tests) being restructured into the new configless design.

## Getting started

### Install

```bash
go get github.com/mirrorstack-ai/app-module-sdk
```

### Usage

```go
package main

import (
    "github.com/go-chi/chi/v5"
    ms "github.com/mirrorstack-ai/app-module-sdk"
)

func main() {
    ms.Init(ms.Config{
        ID:   "media",
        Name: "Media",
        Icon: "perm_media",
    })

    ms.Platform(func(r chi.Router) {
        r.Get("/items", platform.ListItems)
        r.Post("/items", platform.CreateItem)
    })

    ms.Public(func(r chi.Router) {
        r.Get("/items", public.ListItems)
    })

    ms.Start()
}
```

No config files. No YAML. Code is the single source of truth.

### Struct API (for testing)

```go
mod, err := ms.New(ms.Config{ID: "media", Name: "Media", Icon: "perm_media"})

mod.Platform(func(r chi.Router) { ... })
mod.Public(func(r chi.Router) { ... })

mod.Start()
```

## Runtime

`ms.Start()` auto-detects the environment. One binary, works everywhere:

| Environment | What happens | How platform calls |
|------------|-------------|-------------------|
| **Local dev** | HTTP server on `:8080` | HTTP proxy |
| **AWS Lambda** | Lambda handler | Lambda Invoke SDK (VPC-internal) |

Port defaults to `8080`. Override with `PORT` env var.

## Roadmap

### Core

- [x] `ms.Init()` / `ms.New()` — module registration
- [x] `ms.Start()` — runtime auto-detection (HTTP server / Lambda handler)
- [x] `ms.Platform()` / `ms.Public()` / `ms.Internal()` — auth scopes (middleware in #4)

### Platform resources

- [ ] `ms.DB(ctx)` — multi-tenant PostgreSQL (schema-per-app)
- [ ] `ms.Storage(ctx)` — S3 primary + R2 CDN cache (`NoCache` for direct S3)
- [ ] `ms.Cache(ctx)` — scoped Redis (ElastiCache Serverless)
- [ ] `ms.Meter(ctx)` — custom usage metrics for billing

### Events & scheduling

- [ ] `ms.OnEvent()` — subscribe to events from other modules
- [ ] `ms.Emit()` — declare emitted events
- [ ] `ms.Cron()` — register scheduled jobs

### System routes (`/__mirrorstack/`)

- [ ] `platform/health` — health check
- [ ] `platform/manifest` — module identity + capabilities
- [ ] `platform/usage` — usage metrics for billing
- [ ] `platform/lifecycle/install` — fresh install on an app
- [ ] `platform/lifecycle/upgrade` — upgrade between versions
- [ ] `platform/lifecycle/downgrade` — rollback between versions
- [ ] `platform/lifecycle/uninstall` — soft-delete removal

### MCP integration

- [ ] `ms.MCPTool()` — register MCP tools for AI agents
- [ ] `ms.MCPResource()` — register MCP resources
- [ ] `/__mirrorstack/mcp/` — MCP protocol routes

## SDK structure

```
app-module-sdk/
  mirrorstack.go               Config, Module, Init/Start, convenience API
  mirrorstack_test.go          All tests
  internal/
    runtime/                   Lambda/HTTP detection, Lambda handler
  db/                          Multi-tenant PostgreSQL (planned)
  storage/                     S3 + R2 CDN cache (planned)
  cache/                       Scoped Redis (planned)
  meter/                       Custom usage metrics (planned)
  mcp/                         MCP tool/resource registration (planned)
```

## Module structure

```
app-mod-{name}/
  sql/                         Migrations (auto-applied)
    0000_initial.up.sql
    0000_initial.down.sql
  api/                         Go backend
    cmd/main.go
    handler/
      platform/                Dashboard routes
      public/                  End-user routes
      internal/                Events, cron
    service/                   Business logic
  web/                         React frontend (Module Federation)
    platform/pages/
    app/pages/
```

## Tech stack

- **Go 1.26** with [chi v5](https://github.com/go-chi/chi) router
- **AWS Lambda** via auto-detection
- **PostgreSQL** with schema-per-app isolation (Aurora Serverless v2)
- **S3** + **Cloudflare R2** for storage (S3 primary, R2 CDN cache)
- **Redis** via ElastiCache Serverless
- **MCP** for AI agent integration

## Contributing

We welcome contributions! See [CONTRIBUTING.md](CONTRIBUTING.md) to get started.

Look for issues labeled [`good first issue`](https://github.com/mirrorstack-ai/app-module-sdk/issues?q=is%3Aopen+label%3A%22good+first+issue%22) for beginner-friendly tasks.

## License

[FSL-1.1-ALv2](LICENSE) — free to use for any purpose except building a competing platform. Converts to Apache 2.0 after 2 years.
