# MirrorStack Module SDK

Go SDK for building modules on [MirrorStack](https://mirrorstack.com), the Agentic CMS platform.

Write handlers. Drop in pages. The SDK handles the rest.

> **Status:** Under active development. See `reference/v2-restored` branch for the original implementation being restructured.

## Quick Start

```bash
mirrorstack init my-module
cd app-mod-my-module
mirrorstack dev
```

Your module is running and mounted on the platform.

## Write a handler

```go
// api/handler/platform/items.go
package platform

import (
    "net/http"
    ms "github.com/mirrorstack-ai/app-module-sdk"
)

func ListItems(w http.ResponseWriter, r *http.Request) {
    ctx := ms.Context(r)
    rows, _ := ms.DB(ctx).Query(ctx, "SELECT id, title FROM items")
    defer rows.Close()
    ms.JSON(w, 200, ms.ScanAll[Item](rows))
}
```

The SDK reads platform headers, scopes the database to the app's schema, and applies auth middleware automatically. You write business logic. Nothing else.

## Module structure

```
app-mod-{name}/
  mirrorstack.yaml              # Identity (5 lines)
  sql/                          # Migrations (auto-applied on install)
    0000_initial.up.sql
  api/                          # Go backend
    handler/
      platform/                 # Dashboard routes  — auto-applies platform auth
      admin/                    # Admin routes      — auto-applies admin auth
      public/                   # Public routes     — optional auth
      internal/                 # Events + cron     — internal auth
    service/                    # Business logic (optional)
    db/                         # sqlc queries (optional)
  web/                          # React frontend
    platform/
      pages/                    # File-based routing
        {resource}/page.tsx           → /{resource}
        {resource}/[id]/page.tsx      → /{resource}/:id
        settings/{name}/page.tsx      → /settings/{name}
      contributions/            # UI injected into other modules
        {slot}/{id}.tsx
    app/
      pages/                    # End-user pages (same conventions)
```

Files are routes. Directories are auth scopes. SQL files are migrations.

## mirrorstack.yaml

Most modules need 5 lines:

```yaml
id: bookmark
name: Bookmarks
description: Let users bookmark content
icon: bookmark
category: content
```

Pages, nav items, and contributions are derived from the file tree.
Add explicit config only for events, schedules, dependencies, or resource declarations:

```yaml
id: video
name: Video
description: Video hosting and streaming
icon: play_circle
category: content

dependencies: [media, oauth]

events:
  emits: [created, published, transcode_completed]
  subscribes:
    oauth.user_deleted: /events/on-user-deleted

schedules:
  - name: cleanup-temp
    cron: "0 3 * * *"
    handler: /cron/cleanup

resources:
  s3: true
  redis: true
```

## Packages

| Package | Purpose | Import when... |
|---------|---------|----------------|
| `mirrorstack` | Bootstrap, config, context | Always |
| `db` | Multi-tenant PostgreSQL (schema-per-app) | Storing data |
| `cache` | Scoped Redis cache | Caching |
| `file` | S3/R2 presigned URLs | File uploads/downloads |
| `event` | Inter-module emit/call/subscribe | Talking to other modules |
| `meter` | Usage tracking | Custom metering |
| `respond` | JSON, errors, pagination | HTTP responses |
| `mcp` | MCP tool/resource registration | AI agent capabilities |
| `sdktest` | Mock context, test harness | Writing tests |

## Auth scopes

The SDK applies auth based on handler directory:

| Directory | Auth | Who |
|-----------|------|-----|
| `handler/platform/` | Platform user (JWT) | App owner on dashboard |
| `handler/admin/` | Client JWT + admin role | App staff |
| `handler/public/` | Optional client JWT | Anyone |
| `handler/internal/` | Internal secret | Platform-to-module calls |

No middleware code to write. Place your handler in the right directory.

## Platform resources

Modules never get raw credentials. Access is scoped and proxied:

```go
// Database — auto-scoped to app's schema
db := ms.DB(ctx)
rows, _ := db.Query(ctx, "SELECT * FROM items")

// Object storage
url, _ := ms.S3(ctx).PresignPut("uploads/photo.jpg", 15*time.Minute)

// Cache
ms.Cache(ctx).Set("views:"+id, "42", 5*time.Minute)

// Events
ms.Emit(ctx, "created", map[string]any{"itemId": id})
```

## For AI agents

This SDK is designed to be used by Claude Code and other AI agents.

- File paths encode routing, auth, and UI registration
- `mirrorstack.yaml` is minimal (identity only)
- No wiring code, no framework ceremony
- Generate SQL, write handlers, drop in pages

See [CLAUDE.md](CLAUDE.md) for the full agent guide.

## Community

- [Slack](https://join.slack.com/t/mirrorstackai/shared_invite/zt-3twmj15cm-EPfQscE71I~JJj0yHK6EZg)
- [Issues](https://github.com/mirrorstack-ai/app-module-sdk/issues)

## License

Apache 2.0
