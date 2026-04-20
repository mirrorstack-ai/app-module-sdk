# Agent tools

> Language: [English](../../concepts/agent-tools.md) · **繁體中文**

MirrorStack 定位是 **Agentic CMS** — 每個 module 都要能被 AI agent 操作,不只是給人類 UI 用。每個 module 至少要公開一個 agent 可呼叫的 tool 或 resource,這是基本要求 — 所以 template 裡的 `mcp.go` 一定存在,不能透過 CLI flag 關掉。

這份文件說明每個 module 的 MCP surface、tool 跟 resource 的差別,以及區分好 tool 跟爛 tool 的幾個設計原則。

## MCP tools + resources

`ms.MCPTool` 註冊 agent 可以呼叫的操作;`ms.MCPResource` 註冊 agent 可以讀取的資料快照。SDK 在 Internal scope 下提供這些 routes(`/__mirrorstack/mcp/*`),並把 schema 發布到 manifest。

平台的 MCP aggregator 讀取每個已安裝 module 的 manifest,對 agent 提供**一個統一的 MCP server**,每個 tool 都會加上 module 前綴:`{module-id}.{tool-name}`。任何會講 MCP 的 client(Claude Code、Cursor、ChatGPT Desktop、自行開發的 agent runtime)都可以連接。

## Tool vs Resource

| | Tool | Resource |
|---|---|---|
| 副作用 | 有 | 沒有 — 唯讀快照 |
| 輸入參數 | 任意 struct(JSON Schema) | 沒有 |
| 公開的 schema | 輸入 + 輸出 | 只有輸出 |
| 典型用途 | `upload_video`、`transcode`、`send_email` | `get_status`、`list_quotas`、`current_config` |

**判斷原則**:同樣的參數呼叫兩次,會產生額外的狀態變化嗎?會的話就是 tool。只是重複讀取就是 resource。

## 平台如何聚合

```
     ┌──────────────────────────────────────────────────┐
     │      平台 MCP aggregator(單一 endpoint)         │
     │   對外公開:video.transcode、user.create、       │
     │            billing.charge、oauth.sign_in、…      │
     └──────────────────────────────────────────────────┘
                ▲                ▲                ▲
                │ Internal       │ Internal       │ Internal
                │ scope + HMAC   │ scope + HMAC   │ scope + HMAC
                │                │                │
     ┌──────────┴─┐    ┌─────────┴─┐    ┌─────────┴─┐
     │  video     │    │   user    │    │  billing  │
     │  module    │    │  module   │    │  module   │
     └────────────┘    └───────────┘    └───────────┘
```

1. Agent 向 aggregator 呼叫 `video.transcode`。
2. Aggregator 查出 `video` module 的 endpoint,加上 Internal auth。
3. 轉發到 module 的 `/__mirrorstack/mcp/tools/call`。
4. Module handler 用 aggregator 附上的使用者身分執行。

## 信任邊界

三層邊界,每次 hop 一層:

1. **Agent → Aggregator** — 向 aggregator 證明使用者身分(session、API key,看 host 用什麼)。
2. **Aggregator → Module** — Internal scope 認證(`X-MS-Internal-Secret`)。Module 信任 aggregator 已經驗過身分。
3. **Module → DB / storage** — 以 per-app 憑證連線,scope 限制在這個 request 所屬的 app。

**Module handler 絕對不能信任 agent 自己宣稱的身分欄位**(例如 args 裡面傳進來的 `userId`)。要取得身分一律透過 `auth.Get(ctx)` — 這才是 aggregator 驗過的。

## 設計原則

- **小而專一的 tool**。`upload_video` 優於 `do_video_stuff`。Agent 組合小 tool 的能力遠勝於操作大而全的 tool。
- **Typed schemas 配上清楚的 description**。用 `jsonschema:"description=..."` struct tag — agent 會把它當成文件來讀。
- **盡量 idempotent**。網路不穩時,`create_or_update` 比 `create` + `update` 好用。
- **錯誤訊息要明確**。`"quota exceeded — try again after 2025-01-15"` 遠勝於 `"error 503"`。Agent 會讀錯誤字串並調整行為。
- **沒有輸出也可以**。有些 tool 做完事情就結束。回傳 `struct{}{}` 或最小的 ack 即可。
- **人看得到的,agent 也要看得到**。Dashboard 上顯示的資料,通常也該是 MCPResource。Agent 不該比 UI 是二等公民。

## 完整範例

一個 video module 提供:

```go
// Tool — 有副作用
type TranscodeArgs struct {
    VideoID string `json:"videoId" jsonschema:"description=videos table 的 ID"`
    Preset  string `json:"preset"  jsonschema:"description=hd、sd、或 audio_only"`
}
type TranscodeResult struct {
    JobID string `json:"jobId"`
}
ms.MCPTool("transcode", "把一個 video 排進 transcode 佇列",
    func(ctx context.Context, a TranscodeArgs) (TranscodeResult, error) {
        jobID, err := queueTranscode(ctx, a.VideoID, a.Preset)
        return TranscodeResult{JobID: jobID}, err
    })

// Resource — 唯讀
type TranscodeStatus struct {
    JobID    string `json:"jobId"`
    Progress int    `json:"progressPercent"`
    State    string `json:"state"` // queued | running | done | failed
}
ms.MCPResource("transcode_status", "目前進行中的 transcode jobs",
    func(ctx context.Context) ([]TranscodeStatus, error) {
        return listActiveJobs(ctx)
    })
```

Agent 收到「把我最新的 video 轉成 HD」這個 prompt 時會:

1. 呼叫 `video.search({q: "latest"})` — 找出 ID。
2. 呼叫 `video.transcode({videoId, preset: "hd"})` — 排進佇列。
3. 讀取 `video.transcode_status` 直到 State 變成 `done`。

每一步都能從 manifest 裡的 tool/resource description 看出來。Agent 不需要事先知道 module 的細節,description 就夠了。

---

## 相關文件

- [API reference — Agent surface (MCP)](../api-reference.md) — Function signature 與用法。
- [Manifest](./manifest.md) — MCP surface 在 manifest payload 裡的呈現。
- [Scopes](./scopes.md) — 為什麼 MCP routes 放在 Internal scope。
