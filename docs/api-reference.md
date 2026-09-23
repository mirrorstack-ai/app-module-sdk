# API reference

> Language: **English** · [繁體中文](./zh-TW/api-reference.md)

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
| `ms.DependsOn(spec string)` | Declare a REQUIRED dependency. Spec is `"id"` or `"id@constraint"` (npm-style SemVer). Called once at module init. |
| `ms.Needs(spec string, h HandlerFunc) HandlerFunc` | Wrap a handler; declares the spec as an OPTIONAL dependency. Same spec syntax. Returns handler unchanged. |
| `ms.Resolve[T any](id) (T, bool)` | Typed runtime lookup for optional deps (stub in v1). |

```go
ms.Describe("Video upload + HLS streaming")
ms.DependsOn("oauth-core@^1.2.0")                                       // required, SemVer ^1.2.0
ms.OnEvent("video.completed", ms.Needs("video@^1", onVideoCompleted))   // optional
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

## Trusted invocation

The `invocation` package exposes the complete read-only context installed after
the SDK authenticates and validates a platform request. See
[Trusted invocation context](./concepts/invocation.md).

| Function | Purpose |
|---|---|
| `invocation.FromContext(ctx)` | Return a defensive copy of the canonical v1 invocation, when present. |

```go
import "github.com/mirrorstack-ai/app-module-sdk/invocation"

trusted, ok := invocation.FromContext(r.Context())
```

There is no public setter or wire decoder. Routine identity and authorization
should continue to use `ms.UserID`, `ms.AppID`, `ms.AppRole` and
`ms.RequirePermission`.

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

Task registration declares a platform-managed background handler. Managed runners claim one attempt from the task broker and exit after one terminal callback.
Under `mirrorstack dev`, enqueue returns immediately and the handler runs in a process-local executor; the same status and cancellation APIs operate on that local job.

| Function | Purpose |
|---|---|
| `ms.OnTask(name, handler)` | Register a task handler (`func(ctx, json.RawMessage) error`). |
| `ms.RunTask(ctx, name, payload)` | Create a managed task job (returns job ID). |
| `ms.RunTaskWithIdempotencyKey(ctx, name, payload, key)` | Create or recover the same managed task job using a persisted UUID key. |
| `ms.TaskStatus(ctx, jobID)` | Read an app/module-scoped managed job status. |
| `ms.CancelTask(ctx, jobID)` | Request idempotent queued/running job cancellation. |
| `ms.WithCompute(...)` | Declare Standard, Heavy, or platform-admitted GPU compute (`g5g.xlarge`: 4 vCPU, 7168 MiB allocatable). |
| `ms.WithEphemeralStorage(size)` | Declare whole-GiB Heavy/Fargate scratch (21–200 GiB; 20 GiB is implicit). GPU host scratch is fixed by the platform. |
| `ms.Permanent(err)` | Mark a returned handler error as non-retryable. |

```go
ms.OnTask("transcode", handleTranscode,
    ms.WithCompute(ms.Heavy(ms.Res{VCPU: 4, MemoryMB: 8192})),
    ms.WithEphemeralStorage(80 * ms.GiB),
    ms.WithTimeout(2 * time.Hour),
)
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

## Audit

| Function | Purpose |
|---|---|
| `audit.Record(ctx, q, entry)` | Append a module-owned fact and the request's private authenticated proof to the current app's outbox. Use the mutation's transaction querier. |
| `ms.DrainAudit(ctx)` | Claim and forward one bounded outbox batch. Safe for concurrent cron/worker calls; `ms.Tx` also attempts it after commit. Scoped database connections and pool mappings are released before network delivery. |

```go
ms.Tx(r.Context(), func(q db.Querier) error {
    if _, err := q.Exec(r.Context(), "UPDATE items SET state = $1 WHERE id = $2", "ready", id); err != nil {
        return err
    }
    return audit.Record(r.Context(), q, audit.Entry{
        SubjectKind: "item", SubjectID: id, Action: "readied",
    })
})
```

`audit.Entry` has no actor or provenance field. API Platform derives trusted
identity from the original signed invocation. Without one, `audit.Record`
returns `audit.ErrProvenanceUnavailable` before SQL. See
[Durable audit records](./concepts/audit.md).

## Cache / Storage / Meter

| Function | Purpose |
|---|---|
| `ms.Cache(ctx)` | Per-app Redis client. |
| `ms.RequireStorage()` | Declare during startup that this module needs its per-app object-storage namespace. Undeclared modules receive no credential. |
| `ms.Storage(ctx)` | Resolve the declared per-app object storage for this request. S3 is the origin with presigned multipart upload; reads may be served through the configured CDN. Returns `storage.ErrNotDeclared` if startup omitted `ms.RequireStorage()`. |
| `ms.Meter(name, opts...)` | DECLARE a usage metric once in setup. The kind is an OPTION (`ms.Counter`/`ms.Gauge`); also `ms.Unit`/`ms.Price`. A custom metric MUST pass exactly one kind. A reserved `infra.*`/`platform.*` metric may pass `ms.Price` ONLY (a customer-passthrough override; kind/unit are platform-owned). Registers it into the manifest; returns nothing. |
| `ms.Record(ctx, name, value)` | Emit a usage event BY NAME for a declared metric. Mirrors `ms.Emits`/`ms.Emit`; errors on an undeclared name and on a reserved `infra.*`/`platform.*` name (platform-measured, never self-reported). |

```go
// setup
ms.RequireStorage()
ms.Meter("transcode.minutes", ms.Counter, ms.Unit("minute"), ms.Price(50_000))
ms.Meter("infra.compute.ms", ms.Price(0)) // reserved: absorb platform compute (price-only override)
// handler
ms.Record(r.Context(), "transcode.minutes", 12)
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
