# Getting started

> Language: [English](../getting-started.md) · **繁體中文**

5 分鐘內把你的第一個 MirrorStack module 跑起來。

## 準備

- Go 1.26+
- 本機 Postgres(選用 — 只在用到 `ms.DB` / `ms.ModuleDB` 時才需要)
- 設定 `MS_INTERNAL_SECRET` 環境變數(任何字串都行,Internal scope routes 會驗證)

## 1. 複製 template

```bash
cp -r path/to/app-module-sdk/examples/template ./my-module
cd my-module
```

修改 `main.go` — 把 `Config.ID`、`Config.Name`、`Config.Icon` 和 `ms.Describe(...)` 改成你 module 的內容。

## 2. 刪掉不需要的 feature 檔案

Template 採用 hook pattern,每個 feature 會自己註冊自己,所以直接刪檔案就好,`main.go` 不用動。

| 檔案 | 提供的功能 |
|---|---|
| `events.go` | `ms.OnEvent` + `ms.Emits` |
| `schedule.go` | `ms.Cron` |
| `tasks.go` | `ms.OnTask`(ECS task worker mode) |
| `storage.go` | `ms.Storage`(S3 當 origin + presigned multipart upload;讀取走 R2 + Cloudflare Worker cache) |
| `cache.go` | `ms.Cache`(Redis) |
| `meter.go` | `ms.Meter`(計費事件) |
| `module_db.go` | `ms.ModuleDB` / `ms.ModuleTx`(`mod_<id>` schema) |

`main.go`、`mcp.go`、`routes.go` 要保留 — agent 用的 MCP tools 和基本 routes 是 baseline,不能省略。

## 3. 跑起來

```bash
MS_INTERNAL_SECRET=dev go run .
# template module (template) listening on :8080
```

讀一下 manifest 確認每個 feature 都有註冊成功:

```bash
curl -sH "X-MS-Internal-Secret: dev" \
     http://localhost:8080/__mirrorstack/platform/manifest | jq
```

應該會看到 `description`、`dependencies`、`routes`、`mcp.tools` 這些欄位都有值。

## 4. 宣告相依

在 `main.go` 裡,`ms.Describe(...)` 之後加上 required 相依:

```go
ms.DependsOn("oauth-core")   // required — catalog 會先安裝這個
```

Optional 相依用 `ms.Needs(id, handler)` 包住會用到它的 handler:

```go
ms.OnEvent("user.created", ms.Needs("user", onUserCreated))
```

`ms.Needs` 把 `user` 註冊為 optional 相依,並原封不動地回傳 handler。`ms.Cron`、chi route 都能用同一種寫法。完整規則看 [concepts/dependencies.md](./concepts/dependencies.md)。

## 5. 註冊一個 agent tool

`mcp.go` 已經有 `greet` 範例。換成你 module 想提供給 AI agent 的 tool:

```go
type SearchArgs struct {
    Query string `json:"query" jsonschema:"description=自由文字搜尋"`
    Limit int    `json:"limit,omitempty"`
}
type SearchResult struct { Items []string `json:"items"` }

ms.MCPTool("search", "以名稱搜尋 items",
    func(ctx context.Context, a SearchArgs) (SearchResult, error) {
        return SearchResult{Items: findItems(a.Query, a.Limit)}, nil
    })
```

JSON Schema 會從 `SearchArgs` 跟 `SearchResult` 的 struct fields 自動產生。

## 接下來

- [API reference](./api-reference.md) — 每個 `ms.*` function 搭配一行範例。
- [Scopes](./concepts/scopes.md) — Platform / Public / Internal 怎麼選。
- [Manifest](./concepts/manifest.md) — catalog 能從你 module 看到哪些資訊。
