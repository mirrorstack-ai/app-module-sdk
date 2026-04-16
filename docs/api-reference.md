# API reference

Every public `ms.*` function. Grouped by concern. Each has a one-line example.

All functions come in two forms: a package-level convenience wrapper (uses the default `Module` created by `ms.Init`) and a receiver method on `*ms.Module` (for testing or multi-module programs). Only the package-level form is shown below; append `m.` or `(*Module).` for the method.

---

## Lifecycle

| Function | Purpose |
|---|---|
| `ms.Init(Config) error` | Create the default module. Call first. |
| `ms.Start() error` | Auto-detect runtime (Lambda / task worker / HTTP dev) and serve. |
| `ms.Config` | `{ID, Name, Icon, SQL fs.FS, Versions map[string]MigrationVersions}` |

```go
ms.Init(ms.Config{ID: "media", Name: "Media", Icon: "perm_media", SQL: sqlFS})
ms.Start()
```

## Identity

| Function | Purpose |
|---|---|
| `ms.Describe(s string)` | Set the agent-discovery description. |
| `ms.DependsOn(id string)` | Declare a dependency. Required if called from `main.main` or `pkg.init`; optional otherwise. |
| `ms.Resolve[T any](id) (T, bool)` | Typed runtime lookup for optional deps (stub in v1). |

```go
ms.Describe("Video upload + HLS streaming")
ms.DependsOn("oauth-core")   // required
```

## HTTP scopes

See [concepts/scopes.md](./concepts/scopes.md) for the auth boundaries.

| Function | Purpose |
|---|---|
| `ms.Platform(fn)` | Authenticated dashboard users. |
| `ms.Public(fn)` | Anonymous endpoints (webhooks, OAuth callbacks). |
| `ms.Internal(fn)` | Platform-to-module only (HMAC-signed). |
| `ms.RequirePermission(name, roles...)` | Chi middleware + declares the permission in the manifest. Roles are typed values from the `roles` package. |

```go
import p "github.com/mirrorstack-ai/app-module-sdk/roles"

ms.Platform(func(r chi.Router) {
    r.With(ms.RequirePermission("video.upload", p.Admin())).Post("/videos", uploadVideo)
    r.With(ms.RequirePermission("video.view",   p.Admin(), p.Viewer())).Get("/videos", listVideos)
    r.With(ms.RequirePermission("video.moderate", p.Custom("moderator"))).Post("/flag", flagVideo)
})
```

Canonical roles: `p.Admin()`, `p.Viewer()`. Use `p.Custom("key")` for module-specific roles.

## Events

| Function | Purpose |
|---|---|
| `ms.OnEvent(name, handler)` | Subscribe to another module's event (delivered via Internal POST). |
| `ms.Emits(names...)` | Declare event names this module emits. |

```go
ms.Emits("video.completed")
ms.OnEvent("user.created", onUserCreated)
```

## Scheduled jobs

| Function | Purpose |
|---|---|
| `ms.Cron(name, expr, handler)` | Register a cron job. Platform scheduler POSTs to Internal route. |

```go
ms.Cron("cleanup", "0 3 * * *", cleanupHandler)
```

## Background tasks

Task registration enables ECS task worker mode — module is also deployed as a long-running process polling SQS.

| Function | Purpose |
|---|---|
| `ms.OnTask(name, handler)` | Register a task handler (`func(ctx, json.RawMessage) error`). |
| `ms.RunTask(ctx, name, payload)` | Enqueue a task (returns SQS message ID). |

```go
ms.OnTask("transcode", handleTranscode)
ms.RunTask(ctx, "transcode", json.RawMessage(`{"videoId":"abc"}`))
```

## Data

| Function | Purpose |
|---|---|
| `ms.DB(ctx)` | Per-app DB connection (app_<id> schema). |
| `ms.Tx(ctx, fn)` | Per-app transaction. |
| `ms.ModuleDB(ctx)` | Per-module DB connection (mod_<id> schema). |
| `ms.ModuleTx(ctx, fn)` | Per-module transaction. |

```go
ms.Tx(ctx, func(q db.Querier) error {
    _, err := q.Exec(ctx, "INSERT INTO items (name) VALUES ($1)", name)
    return err
})
```

## Cache / Storage / Meter

| Function | Purpose |
|---|---|
| `ms.Cache(ctx)` | Per-app Redis client. |
| `ms.Storage(ctx)` | Per-app S3 storage with presigned multipart. |
| `ms.Meter(ctx).Record(metric, value)` | Emit a billing event via async Lambda invoke. |

```go
ms.Meter(r.Context()).Record("transcode.minutes", 12)
```

## Agent surface (MCP)

| Function | Purpose |
|---|---|
| `ms.MCPTool[In, Out](name, description, handler)` | Agent-callable tool. Schemas derived from types. |
| `ms.MCPResource[Out](name, description, handler)` | Agent-readable resource. |

```go
type GreetArgs struct { Name string `json:"name"` }
type GreetResult struct { Message string `json:"message"` }

ms.MCPTool("greet", "Say hi",
    func(ctx context.Context, a GreetArgs) (GreetResult, error) {
        return GreetResult{Message: "hi " + a.Name}, nil
    })
```

MCP routes served under Internal scope at `/__mirrorstack/mcp/tools/{list,call}` and `/resources/{list,read}`.

## System routes (auto-mounted)

The SDK mounts these under `/__mirrorstack/` automatically:

- `GET /health` — public, no auth
- `GET /platform/manifest` — internal, returns full manifest
- `POST /platform/lifecycle/{app,module}/{install,upgrade,downgrade,uninstall}` — internal, drives migrations
- MCP routes listed above

You don't register these; they come free with `ms.Start()`.
