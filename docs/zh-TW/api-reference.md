# API reference

> Language: [English](../api-reference.md) · **繁體中文**

每個 public 的 `ms.*` function。依功能分組,每個都配一行 example。

所有 function 都有兩種型態:package-level 的 convenience wrapper(用 `ms.Init` 建立的 default `Module`),還有 `*ms.Module` 的 receiver method(給 testing 或多 module 程式用)。下面只列 package-level 的,receiver method 就是前面加 `m.` 或 `(*Module).`。

---

## Lifecycle

| Function | 用途 |
|---|---|
| `ms.Init(Config) error` | 建立 default module。一開始就要 call。 |
| `ms.Start() error` | Auto-detect runtime(Lambda / task worker / HTTP dev)然後開始 serve。 |
| `ms.Config` | `{ID, Name, Icon, SQL fs.FS, Versions map[string]MigrationVersions}` |

```go
ms.Init(ms.Config{ID: "media", Name: "Media", Icon: "perm_media", SQL: sqlFS})
ms.Start()
```

## Identity

| Function | 用途 |
|---|---|
| `ms.Describe(s string)` | 設定給 agent discovery 用的 description。 |
| `ms.DependsOn(id string)` | Declare dependency。從 `main.main` 或 `pkg.init` 裡 call 就是 required;其他地方就是 optional。 |
| `ms.Resolve[T any](id) (T, bool)` | Runtime 查 optional deps 的 typed lookup(v1 是 stub)。 |

```go
ms.Describe("Video upload + HLS streaming")
ms.DependsOn("oauth-core")   // required
```

## HTTP scopes

Auth 的 boundary 看 [concepts/scopes.md](./concepts/scopes.md)。

| Function | 用途 |
|---|---|
| `ms.Platform(fn)` | 給 dashboard users(有登入的)。 |
| `ms.Public(fn)` | 匿名 endpoints(webhooks、OAuth callbacks)。 |
| `ms.Internal(fn)` | Platform 才能 call(HMAC-signed)。 |
| `ms.RequirePermission(name, roles...)` | Chi middleware + 把 permission 註冊到 manifest。 |

```go
import p "github.com/mirrorstack-ai/app-module-sdk/roles"

ms.Platform(func(r chi.Router) {
    r.With(ms.RequirePermission("video.upload", p.Admin())).Post("/videos", uploadVideo)
})
```

## Events

| Function | 用途 |
|---|---|
| `ms.OnEvent(name, handler)` | Subscribe 其他 module 的 event(Internal POST 送進來)。 |
| `ms.Emits(names...)` | Declare 你 module 會 emit 哪些 event。 |

```go
ms.Emits("video.completed")
ms.OnEvent("user.created", onUserCreated)
```

## Scheduled jobs

| Function | 用途 |
|---|---|
| `ms.Cron(name, expr, handler)` | Register 一個 cron job。Platform scheduler 會 POST 到 Internal route。 |

```go
ms.Cron("cleanup", "0 3 * * *", cleanupHandler)
```

## Background tasks

Register task 會讓 module 也以 ECS task worker mode 部署(一個 long-running process 在 poll SQS)。

| Function | 用途 |
|---|---|
| `ms.OnTask(name, handler)` | Register 一個 task handler(`func(ctx, json.RawMessage) error`)。 |
| `ms.RunTask(ctx, name, payload)` | 把 task enqueue 進去(回傳 SQS message ID)。 |

```go
ms.OnTask("transcode", handleTranscode)
ms.RunTask(ctx, "transcode", json.RawMessage(`{"videoId":"abc"}`))
```

## Data

| Function | 用途 |
|---|---|
| `ms.DB(ctx)` | Per-app DB connection(app_<id> schema)。 |
| `ms.Tx(ctx, fn)` | Per-app transaction。 |
| `ms.ModuleDB(ctx)` | Per-module DB connection(mod_<id> schema)。 |
| `ms.ModuleTx(ctx, fn)` | Per-module transaction。 |

```go
ms.Tx(ctx, func(q db.Querier) error {
    _, err := q.Exec(ctx, "INSERT INTO items (name) VALUES ($1)", name)
    return err
})
```

## Cache / Storage / Meter

| Function | 用途 |
|---|---|
| `ms.Cache(ctx)` | Per-app Redis client。 |
| `ms.Storage(ctx)` | Per-app S3 storage 配 presigned multipart。 |
| `ms.Meter(ctx).Record(metric, value)` | 用 async Lambda invoke emit billing event。 |

```go
ms.Meter(r.Context()).Record("transcode.minutes", 12)
```

## Agent surface(MCP)

| Function | 用途 |
|---|---|
| `ms.MCPTool[In, Out](name, description, handler)` | Agent 可以 call 的 tool。Schema 從 type 自動推導。 |
| `ms.MCPResource[Out](name, description, handler)` | Agent 可以讀的 resource。 |

```go
type GreetArgs struct { Name string `json:"name"` }
type GreetResult struct { Message string `json:"message"` }

ms.MCPTool("greet", "跟人打招呼",
    func(ctx context.Context, a GreetArgs) (GreetResult, error) {
        return GreetResult{Message: "hi " + a.Name}, nil
    })
```

MCP routes 放在 Internal scope 下面:`/__mirrorstack/mcp/tools/{list,call}` 跟 `/resources/{list,read}`。

## System routes(SDK 自動 mount)

SDK 會自動把下面這些掛在 `/__mirrorstack/` 底下:

- `GET /health` — public,沒有 auth
- `GET /platform/manifest` — internal,回傳完整的 manifest
- `POST /platform/lifecycle/{app,module}/{install,upgrade,downgrade,uninstall}` — internal,驅動 migrations
- 上面列的 MCP routes

你不用自己 register 這些,`ms.Start()` 就包含了。
