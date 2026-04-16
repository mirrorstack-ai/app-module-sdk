# Getting started

> Language: [English](../getting-started.md) · **繁體中文**

5 分鐘內把你的第一個 MirrorStack module 跑起來。

## 你需要準備

- Go 1.26+
- 一個本機的 Postgres(optional — 只有在你用 `ms.DB` / `ms.ModuleDB` 的時候才需要)
- 設好 `MS_INTERNAL_SECRET` 環境變數(任何字串都行,內部 scope routes 需要)

## 1. 把 template copy 過來

```bash
cp -r path/to/app-module-sdk/examples/template ./my-module
cd my-module
```

改 `main.go` — 把 `Config.ID`、`Config.Name`、`Config.Icon`、還有 `ms.Describe(...)` 換成你 module 自己的內容。

## 2. 刪掉你不需要的 features

template 用 hook pattern,每個 feature 自己 register 自己,所以你直接刪檔案就好,`main.go` 不用動。

| 檔案 | 加了什麼 |
|---|---|
| `events.go` | `ms.OnEvent` + `ms.Emits` |
| `schedule.go` | `ms.Cron` |
| `tasks.go` | `ms.OnTask`(ECS task worker mode) |
| `storage.go` | `ms.Storage`(S3 當 origin + presigned multipart upload;read 走 R2 + Cloudflare Worker cache) |
| `cache.go` | `ms.Cache`(Redis) |
| `meter.go` | `ms.Meter`(billing events) |
| `module_db.go` | `ms.ModuleDB` / `ms.ModuleTx`(`mod_<id>` schema) |

`main.go`、`mcp.go`、`routes.go` 要留著 — agent 用的 MCP tools 跟基本 routes 是 baseline。

## 3. 跑起來

```bash
MS_INTERNAL_SECRET=dev go run .
# template module (template) listening on :8080
```

測一下 manifest 確定每個 feature 都有 register 到:

```bash
curl -sH "X-MS-Internal-Secret: dev" \
     http://localhost:8080/__mirrorstack/platform/manifest | jq
```

你應該會看到 `description`、`dependencies`、`routes`、`mcp.tools` 這些都有值。

## 4. Declare dependencies

在 `main.go` 裡面,`ms.Describe(...)` 之後加 required deps:

```go
ms.DependsOn("oauth-core")   // required — catalog 會先裝這個
```

Optional deps 要從 helper function 裡面 declare(auto-detect 會處理):

```go
func configureWithUser() {
    ms.DependsOn("user")     // optional — 就算 `user` module 沒裝也沒關係
    ms.OnEvent("user.created", onUserCreated)
}
```

完整的規則看 [concepts/dependencies.md](./concepts/dependencies.md)。

## 5. Register 一個 agent tool

`mcp.go` 裡面已經有 `greet` 的範例。換成你 module 想給 AI agent 用的 tool:

```go
type SearchArgs struct {
    Query string `json:"query" jsonschema:"description=自由文字搜尋"`
    Limit int    `json:"limit,omitempty"`
}
type SearchResult struct { Items []string `json:"items"` }

ms.MCPTool("search", "用名字搜尋 items",
    func(ctx context.Context, a SearchArgs) (SearchResult, error) {
        return SearchResult{Items: findItems(a.Query, a.Limit)}, nil
    })
```

JSON Schema 會從 `SearchArgs` 跟 `SearchResult` 的 struct fields 自動產生。

## 接下來

- [API reference](./api-reference.md) — 每個 `ms.*` function 的一行 example。
- [Scopes](./concepts/scopes.md) — Platform / Public / Internal 怎麼選。
- [Manifest](./concepts/manifest.md) — catalog 看得到你 module 的哪些東西。
