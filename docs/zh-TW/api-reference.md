# API reference

> Language: [English](../api-reference.md) · **繁體中文**

所有公開的 `ms.*` function,依功能分組,每項配一行範例。

每個 function 都有兩種形式:package-level 的 convenience wrapper(操作 `ms.Init` 建立的 default `Module`),以及 `*ms.Module` 的 receiver method(供 testing 或多 module 程式使用)。下表只列 package-level 版本 — receiver method 把前綴換成 `m.` 或 `(*Module).` 即可。

---

## Lifecycle

| Function | 用途 |
|---|---|
| `ms.Init(Config) error` | 建立 default module。要最先呼叫。 |
| `ms.Start() error` | 自動偵測執行模式(Lambda / task worker / HTTP dev),開始提供服務。 |
| `ms.Config` | `{ID, Name, Icon, SQL fs.FS, Versions map[string]MigrationVersions}` |

```go
ms.Init(ms.Config{ID: "media", Name: "Media", Icon: "perm_media", SQL: sqlFS})
ms.Start()
```

## Identity

| Function | 用途 |
|---|---|
| `ms.Describe(s string)` | 設定 agent 探索用的描述文字。 |
| `ms.DependsOn(spec string)` | 宣告 REQUIRED 相依。Spec 是 `"id"` 或 `"id@constraint"`(npm 風格 SemVer)。Module init 時呼叫一次。 |
| `ms.Needs(spec string, h HandlerFunc) HandlerFunc` | 包住一個 handler;把 spec 宣告成 OPTIONAL 相依。Spec 語法相同。Handler 原樣回傳。 |
| `ms.Resolve[T any](id) (T, bool)` | 執行時查詢 optional 相依的 typed lookup(v1 目前是 stub)。 |

```go
ms.Describe("Video upload + HLS streaming")
ms.DependsOn("oauth-core@^1.2.0")                                       // required, SemVer ^1.2.0
ms.OnEvent("video.completed", ms.Needs("video@^1", onVideoCompleted))   // optional
```

## HTTP scopes

認證邊界請看 [concepts/scopes.md](./concepts/scopes.md)。

| Function | 用途 |
|---|---|
| `ms.Platform(fn)` | 給已登入的 dashboard 使用者。 |
| `ms.Public(fn)` | 匿名 endpoint(webhooks、OAuth callbacks)。 |
| `ms.Internal(fn)` | 只有平台能呼叫(HMAC-signed)。 |
| `ms.RequirePermission(name, roles...)` | Chi middleware,同時把 permission 註冊到 manifest。 |

```go
import p "github.com/mirrorstack-ai/app-module-sdk/roles"

ms.Platform(func(r chi.Router) {
    r.With(ms.RequirePermission("video.upload", p.Admin())).Post("/videos", uploadVideo)
})
```

## Trusted invocation

`invocation` package 提供 SDK 在驗證平台 request 後安裝的完整唯讀 context。
詳見 [Trusted invocation context](./concepts/invocation.md)。

| Function | 用途 |
|---|---|
| `invocation.FromContext(ctx)` | 在存在時回傳 canonical v1 invocation 的 defensive copy。 |

```go
import "github.com/mirrorstack-ai/app-module-sdk/invocation"

trusted, ok := invocation.FromContext(r.Context())
```

SDK 不公開 setter 或 wire decoder。一般 identity 與 authorization 繼續使用
`ms.UserID`、`ms.AppID`、`ms.AppRole` 與 `ms.RequirePermission`。

## Events

| Function | 用途 |
|---|---|
| `ms.OnEvent(name, handler)` | 訂閱其他 module 的事件(平台以 Internal POST 派送進來)。 |
| `ms.Emits(names...)` | 宣告本 module 會發出的事件名稱。 |

```go
ms.Emits("video.completed")
ms.OnEvent("user.created", onUserCreated)
```

## Scheduled jobs

| Function | 用途 |
|---|---|
| `ms.Cron(name, expr, handler)` | 註冊一個 cron job。平台排程器會 POST 到對應的 Internal route。 |

```go
ms.Cron("cleanup", "0 3 * * *", cleanupHandler)
```

## Background tasks

註冊 task 會宣告由平台管理的背景 handler。Runner 每次只向 task broker claim 一個 attempt，回報終態後即結束。
在 `mirrorstack dev` 下，enqueue 會立即回傳並由 process-local executor 執行 handler；相同的狀態查詢與取消 API 會操作該 local job。

| Function | 用途 |
|---|---|
| `ms.OnTask(name, handler)` | 註冊 task handler(`func(ctx, json.RawMessage) error`)。 |
| `ms.RunTask(ctx, name, payload)` | 建立 managed task job（回傳 job ID）。 |
| `ms.RunTaskWithIdempotencyKey(ctx, name, payload, key)` | 使用持久化的 UUID key 建立或取回同一個 managed task job。 |
| `ms.TaskStatus(ctx, jobID)` | 讀取限定於目前 app/module 的 job 狀態。 |
| `ms.CancelTask(ctx, jobID)` | 以冪等方式取消等待中或執行中的 job。 |
| `ms.WithCompute(...)` | 宣告 Standard、Heavy 或平台核准的 GPU 運算（`g5g.xlarge`：4 vCPU、7168 MiB 可用記憶體）。 |
| `ms.WithEphemeralStorage(size)` | 以整 GiB 宣告 Heavy（21–200 GiB，20 GiB 為預設）暫存空間；GPU 主機暫存空間由平台固定。 |
| `ms.Permanent(err)` | 將 handler 錯誤標記為不可重試。 |

```go
ms.OnTask("transcode", handleTranscode)
ms.RunTask(ctx, "transcode", json.RawMessage(`{"videoId":"abc"}`))
```

## Data

| Function | 用途 |
|---|---|
| `ms.DB(ctx)` | Per-app DB 連線(`app_<id>` schema)。 |
| `ms.Tx(ctx, fn)` | Per-app transaction。 |
| `ms.ModuleDB(ctx)` | Per-module DB 連線(`mod_<id>` schema)。 |
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
| `ms.RequireStorage()` | 啟動時明確宣告模組需要 per-app 物件儲存命名空間；未宣告的模組不會取得任何儲存憑證。 |
| `ms.Storage(ctx)` | 取得此 request 已宣告的 per-app 物件儲存。S3 為 origin，支援 presigned multipart upload；若啟動時未呼叫 `ms.RequireStorage()`，回傳 `storage.ErrNotDeclared`。 |
| `ms.Meter(ctx).Record(metric, value)` | 以非同步 Lambda invoke 發送計費事件。 |

```go
ms.RequireStorage() // setup
ms.Meter(r.Context()).Record("transcode.minutes", 12)
```

## Agent surface(MCP)

| Function | 用途 |
|---|---|
| `ms.MCPTool[In, Out](name, description, handler)` | Agent 可呼叫的 tool。Schema 從型別自動推導。 |
| `ms.MCPResource[Out](name, description, handler)` | Agent 可讀取的 resource。 |

```go
type GreetArgs struct { Name string `json:"name"` }
type GreetResult struct { Message string `json:"message"` }

ms.MCPTool("greet", "跟人打招呼",
    func(ctx context.Context, a GreetArgs) (GreetResult, error) {
        return GreetResult{Message: "hi " + a.Name}, nil
    })
```

MCP routes 位於 Internal scope 底下:`/__mirrorstack/mcp/tools/{list,call}` 與 `/resources/{list,read}`。

## System routes(SDK 自動掛載)

SDK 會把下列 route 自動掛在 `/__mirrorstack/` 底下:

- `GET /health` — Public,無認證
- `GET /platform/manifest` — Internal,回傳完整 manifest
- `POST /platform/lifecycle/{app,module}/{install,upgrade,downgrade,uninstall}` — Internal,驅動 migrations
- 上面列的 MCP routes

這些不必自行註冊,`ms.Start()` 已經包含。
