# @mirrorstack-ai/app-module-sdk

[![License: FSL-1.1-ALv2](https://img.shields.io/badge/License-FSL--1.1--ALv2-blue.svg)](LICENSE)
[![Good First Issues](https://img.shields.io/github/issues/mirrorstack-ai/app-module-sdk/good%20first%20issue?color=7057ff&label=good%20first%20issues)](https://github.com/mirrorstack-ai/app-module-sdk/issues?q=is%3Aopen+label%3A%22good+first+issue%22)

Go SDK for building modules on [MirrorStack](https://mirrorstack.ai), the Agentic CMS platform.

Built with Go, chi router, and designed for AWS Lambda + local dev.

**[Docs](./docs/)** | **[Template module](./examples/template/)** | **[Changelog](./CHANGELOG.md)** | **[Issues](https://github.com/mirrorstack-ai/app-module-sdk/issues)** | **[Good First Issues](https://github.com/mirrorstack-ai/app-module-sdk/issues?q=is%3Aopen+label%3A%22good+first+issue%22)** | **[Slack](https://join.slack.com/t/mirrorstackai/shared_invite/zt-3twmj15cm-EPfQscE71I~JJj0yHK6EZg)**

> **Status:** Under active development — see the roadmap below.
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
    "github.com/mirrorstack-ai/app-module-sdk/db"
)

func main() {
    ms.Init(ms.Config{
        ID:   "media",
        Name: "Media",
        Icon: "perm_media",
    })

    ms.Platform(func(r chi.Router) {
        r.Get("/items", listItems)
        r.Post("/items", createItem)
    })

    ms.Start()
}

func listItems(w http.ResponseWriter, r *http.Request) {
    conn, release, err := ms.DB(r.Context())
    if err != nil { ... }
    defer release()

    queries := generated.New(conn) // sqlc-generated, type-safe
    items, err := queries.ListItems(r.Context(), params)
}

func createItem(w http.ResponseWriter, r *http.Request) {
    err := ms.Tx(r.Context(), func(q db.Querier) error {
        queries := generated.New(q)
        // reads + writes inside a transaction — atomic
        return queries.CreateItem(r.Context(), params)
    })
}
```

No config files. No YAML. Code is the single source of truth.

### Struct API (for testing)

```go
mod, err := ms.New(ms.Config{ID: "media", Name: "Media", Icon: "perm_media"})

mod.Platform(func(r chi.Router) { ... })
mod.Start()
```

## Runtime

`ms.Start()` auto-detects the environment. One binary, works everywhere:

| Environment | What happens | How platform calls |
|------------|-------------|-------------------|
| **Local dev** | HTTP server on `:8080` | HTTP proxy |
| **AWS Lambda** | Lambda handler | Lambda Invoke SDK (VPC-internal) |

Port defaults to `8080`. Override with `PORT` env var.

> **Warning: never expose the dev-mode HTTP server to the public internet.**
>
> When `AWS_LAMBDA_FUNCTION_NAME` is unset, `InternalAuth` returns 401
> (not 503) on a missing secret to keep local tooling friendly. A dev
> server bound to `0.0.0.0` with no `MS_INTERNAL_SECRET` set will accept
> platform calls from anyone who can reach the port. Use Lambda or ECS
> task worker mode for production. For staging, set `MS_INTERNAL_SECRET`
> and put the server behind a VPN or IP allowlist.

## Database

`ms.DB(ctx)` returns a scoped database connection with automatic tenant isolation:

```go
conn, release, err := ms.DB(r.Context())
defer release()
// queries run against this app's schema only
```

`ms.Tx(ctx, fn)` runs operations in a transaction — atomic commit or rollback:

```go
err := ms.Tx(r.Context(), func(q db.Querier) error {
    // all reads + writes here are atomic
    return nil // commit, or return error to rollback
})
```

### Module-shared schema (`mod_<id>`)

For state that crosses app boundaries — outbox tables, dedup ledgers,
cross-app audit logs, rate limiters, module-wide config — use `ms.ModuleDB`
and `ms.ModuleTx`:

```go
// Insert into the module's shared outbox table
err := ms.ModuleTx(r.Context(), func(q db.Querier) error {
    return q.Exec(ctx, "INSERT INTO outbox (event, payload) VALUES ($1, $2)", ...)
})
```

`ModuleDB` and `ModuleTx` operate on the module's `mod_<id>` schema — independent
of the per-app `app_<id>` schema that `DB`/`Tx` use. A handler that needs both
calls both — they use independent credentials and don't interfere.

The `mod_<id>` schema is where you put `sql/module/*.up.sql` migrations
(see Module structure below).

#### Platform role grant contract

The SDK relies on the platform provisioning two distinct DB roles per
deployed module with disjoint privileges:

| Role | Schema | Granted via |
|------|--------|-------------|
| Per-(module, app) | `app_<appID>` only | injected as `db.Credential` for `Module.DB` / `Module.Tx` |
| Per-module | `mod_<moduleID>` only | injected as `db.Credential` for `Module.ModuleDB` / `Module.ModuleTx` |

Cross-credential contamination (e.g., a misrouted app credential reaching
`ModuleDB`) is caught by Postgres at the SQL layer because the per-(module,
app) role lacks `USAGE` on `mod_<id>` — defense-in-depth, no SDK enforcement
required. Verify your platform-side role provisioning keeps the two grant
sets disjoint.

### Security (3-layer isolation)

| Layer | What | How |
|-------|------|-----|
| **Credentials** | Each (module, app) pair gets its own DB role | Platform injects per-invocation via Lambda payload |
| **Schema** | `SET search_path` per request | SDK sets/resets automatically |
| **RLS** | Row-level policies on `app_id` column | Defense-in-depth, platform-managed |

Module developers don't think about any of this — just call `ms.DB(ctx)`.

## Auth & permissions

Three scopes control who can call your routes:

| Scope | Who | Default |
|-------|-----|---------|
| `ms.Platform()` | Dashboard users (authenticated) | Any authenticated role |
| `ms.Public()` | Anyone (end users, anonymous) | No auth check |
| `ms.Internal()` | Platform only (events, cron) | Validates `MS_INTERNAL_SECRET` |

### Permissions

Use `ms.RequirePermission` for fine-grained role control:

```go
ms.Platform(func(r chi.Router) {
    r.With(ms.RequirePermission("media.view", "admin", "member", "viewer")).Get("/items", listItems)
    r.With(ms.RequirePermission("media.upload", "admin", "member")).Post("/items", uploadItem)
    r.With(ms.RequirePermission("media.delete", "admin")).Delete("/items/{id}", deleteItem)
})
```

3 roles: `admin` | `member` | `viewer`

Permissions are auto-registered for manifest generation — the platform knows what each module requires.

### Context helpers

```go
func handler(w http.ResponseWriter, r *http.Request) {
    a := auth.Get(r.Context())
    a.UserID   // who
    a.AppID    // which app
    a.AppRole  // what access (admin, member, viewer)
}
```

### Query approach

**sqlc as default** — write SQL, generate type-safe Go:

```sql
-- sql/queries/media.sql
-- name: ListItems :many
SELECT id, title FROM media_items ORDER BY created_at DESC LIMIT $1 OFFSET $2;
```

```go
queries := generated.New(conn) // sqlc-generated
items, err := queries.ListItems(ctx, params) // type-safe, no manual scanning
```

**Raw pgx as escape hatch** — for dynamic queries:

```go
conn.Query(ctx, "SELECT * FROM items WHERE title ILIKE $1", "%"+search+"%")
```

## Cache

`ms.Cache(ctx)` returns a scoped Redis client with automatic key prefix isolation:

```go
c, err := ms.Cache(r.Context())
if err != nil { ... }

c.Set("views:123", "42", 5*time.Minute)   // Redis key: app_abc:mod_media:views:123
val, err := c.Get("views:123")             // returns "42"
c.Del("views:123")
```

Keys auto-prefixed: developer writes `"views:123"`, Redis stores `"app_abc123:mod_media:views:123"`. Each app sees only its own cache.

## Roadmap

### Core

- [x] `ms.Init()` / `ms.New()` — module registration
- [x] `ms.Start()` — runtime auto-detection (HTTP server / Lambda handler)
- [x] `ms.Platform()` / `ms.Public()` / `ms.Internal()` — auth scopes with middleware
- [x] `ms.RequirePermission()` — per-route permission with auto-registration for manifest (typed roles: `roles.Admin()`, `roles.Viewer()`, `roles.Custom("...")`)

### Database

- [x] `ms.DB(ctx)` — scoped connection with schema-per-app isolation
- [x] `ms.Tx(ctx, fn)` — atomic transactions
- [x] Per-app credential pools (PoolCache with LRU eviction)
- [x] Schema leak prevention (RESET on release, destroy dirty connections)
- [x] RLS support (`ms.app_id` session variable)
- [x] sqlc-compatible `Querier` interface

### Platform resources

- [x] `ms.Storage(ctx)` — S3 presigned URLs + CDN (R2 cache) + multipart upload
- [x] `ms.Cache(ctx)` — scoped Redis (ElastiCache Serverless)
- [x] `ms.Meter(ctx)` — custom usage metrics for billing

### Events & scheduling

- [x] `ms.OnEvent()` — subscribe to events from other modules
- [x] `ms.Emits()` — declare emitted events
- [x] `ms.Cron()` — register scheduled jobs

### System routes (`/__mirrorstack/`)

- [x] `health` — health check
- [x] `platform/manifest` — module identity + capabilities
- [x] `mcp/tools/{list,call}` and `mcp/resources/{list,read}` — agent tool and resource surface
- [x] `platform/lifecycle/app/install` and `platform/lifecycle/module/install` — fresh install (per-app schema or per-module shared schema)
- [x] `platform/lifecycle/app/upgrade` and `platform/lifecycle/module/upgrade` — upgrade between versions
- [x] `platform/lifecycle/app/downgrade` and `platform/lifecycle/module/downgrade` — rollback between versions
- [x] `platform/lifecycle/app/uninstall` and `platform/lifecycle/module/uninstall` — soft-delete removal

### Agent orchestration

- [x] `ms.Describe()` — module description for agent discovery
- [x] `ms.DependsOn()` — dependency declaration with auto-detected required/optional
- [x] `ms.Resolve[T]()` — typed runtime lookup for optional deps (stub pending cross-module wiring)
- [x] `ms.MCPTool()` — agent-callable tool with JSON Schema derivation
- [x] `ms.MCPResource()` — agent-readable resource
- [x] `/__mirrorstack/mcp/` — MCP protocol routes (tools/resources, list/call/read)

## SDK structure

```
app-module-sdk/
  mirrorstack.go               Config, Module, Init/Start, DB/Tx, scopes, convenience API
  mirrorstack_test.go          All root tests
  auth/
    context.go                 WithUserID/WithAppID/WithAppRole, role constants
    middleware.go              PlatformAuth, PublicAuth, InternalAuth
    permission.go              RequireRoles middleware (no tracking — see Module.RequirePermission)
  db/
    credential.go              Credential struct, context helpers
    db.go                      Dev-mode client, Querier interface
    pool_cache.go              PoolCache (LRU), AcquireScoped
    scope.go                   applyScope/resetScope (batch SET/RESET)
    tx.go                      Transaction support
    db_test.go                 Unit tests
    db_integration_test.go     Integration tests (build tag: integration)
  internal/
    httputil/respond.go        JSON response helper
    runtime/
      detect.go                Lambda detection
      lambda.go                Lambda handler, credential injection
  system/
    health.go                  Health endpoint
  cache/
    credential.go              Cache credential, context helpers
    cache.go                   Client with Set/Get/Del, key prefix, ForApp
  storage/
    credential.go              STS credential, context helpers
    storage.go                 Client with PresignPut/Get, URL (CDN)
    multipart.go               Multipart upload for large files
  meter/                       Custom usage metrics (planned)
  mcp/                         MCP tool/resource registration (planned)
```

## Module structure

```
app-mod-{name}/
  sql/
    app/                       Per-app migrations (one app_<id> schema per tenant)
      0000_initial.up.sql
      0000_initial.down.sql
    module/                    Per-module migrations (single mod_<id> shared schema)
      0000_outbox.up.sql       e.g. outbox, dedup ledgers, audit logs
      0000_outbox.down.sql
    queries/                   sqlc query definitions
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

## Development

```bash
# Start local postgres
docker compose up -d

# Run unit tests
go test ./...

# Run integration tests (requires postgres)
go test -tags integration ./...
```

## Tech stack

- **Go 1.26** with [chi v5](https://github.com/go-chi/chi) router
- **AWS Lambda** via auto-detection
- **PostgreSQL** with schema-per-app isolation (Aurora Serverless v2)
- **pgx v5** for database access, **sqlc** for query generation
- **S3** + **Cloudflare R2** for storage (S3 primary, R2 CDN cache)
- **Redis** via ElastiCache Serverless
- **MCP** for AI agent integration

## Contributing

We welcome contributions! See [CONTRIBUTING.md](CONTRIBUTING.md) to get started.

Look for issues labeled [`good first issue`](https://github.com/mirrorstack-ai/app-module-sdk/issues?q=is%3Aopen+label%3A%22good+first+issue%22) for beginner-friendly tasks.

## License

[FSL-1.1-ALv2](LICENSE) — free to use for any purpose except building a competing platform. Converts to Apache 2.0 after 2 years.
