# Agent tools

> Language: [English](../../concepts/agent-tools.md) · **繁體中文**

MirrorStack 定位是 **Agentic CMS** — 每個 module 都要可以被 AI agent 操作,不是只給人類 UI 用。expose 至少一個 agent-callable 的 tool 或 resource 是 baseline requirement,這就是為什麼 template 裡面 `mcp.go` 永遠都會在,不能被 CLI flag 關掉。

這份文件講 agent access 的三種 pattern、怎麼挑、還有區分好 tool 跟爛 tool 的幾個 design principle。

## 三種 Pattern

### 1. MCP tools + resources(baseline — 每個 module 都要有)

主要的機制。`ms.MCPTool` register agent 可以 call 的操作;`ms.MCPResource` register agent 可以讀的 snapshot。SDK 在 Internal scope 下面 serve 這些 routes(`/__mirrorstack/mcp/*`),還會把 schema 發佈到 manifest。

Platform 的 MCP aggregator 會讀每一個 installed module 的 manifest,然後對 agent expose **一個統一的 MCP server**,每個 tool 都會加上 module 的 prefix:`{module-id}.{tool-name}`。任何會講 MCP 的 client(Claude Code、Cursor、ChatGPT Desktop、自己寫的 agent runtime)都可以 connect。

**什麼時候用 MCP:**
- Action 或 query 在 install 的時候就可以 discover(schema 是穩定的)
- Agent 根據 description 自己決定要 call 哪個 tool
- 想要跨 agent host 都能用

### 2. Claude Code skills(workflow orchestration)

Skills 是 Claude Code 專屬的 markdown 檔案,把多步驟 workflow 包進一個 slash command。Skill 本身不跑 code — 它告訴 Claude 怎麼 orchestrate 其他 primitive(通常是 MCP tools)。

**什麼時候用 skills:**
- Workflow 有 3+ 步驟、而且總是一起發生
- 步驟裡面需要 LLM 做判斷(選哪個 preset、選哪個 user 等等)
- 你要 ship 一個 Claude Code plugin 配有自己的 defaults

Skills 底下 call 的還是 MCP tools。Skills 是 DX sugar;MCP 才是 protocol。

### 3. Subagents(隔離的 task execution)

Claude Code 支援 subagent — 獨立的 LLM instance,有自己的 context window 跟受限的 tool allowlist。適合 main agent 想 delegate 但不想污染自己 context 的場景。

**什麼時候用 subagent:**
- Task 會跑很久或產生一堆中間 output
- Work 可以用便宜/快的 model(batch processing 用 Haiku)
- 需要 guardrail(subagent 只看得到 subset 的 tool)

Subagent 底下 call 的也是 MCP tools — 只是 allowlist 比較窄。

### 怎麼挑

```
問題                                       答案
---------------------------------------------------------------------
我 module 有 expose 功能嗎?                 → Ship MCP tools/resources。
                                             (Required baseline)

我也要出 Claude Code plugin 嗎?             → 常用的 workflow 考慮做成
                                             skill。

某個 flow 會跑很久/很吵/很花錢嗎?           → 包成 subagent,tool
                                             allowlist 限縮。
```

MCP 永遠是 foundation。Skills 跟 subagent 是上面的 opt-in layer。

## Tool vs Resource

| | Tool | Resource |
|---|---|---|
| Side effects | 有 | 沒有 — read-only snapshot |
| Input args | 任意 struct(JSON Schema) | 沒有 |
| Schema expose | Input + output | 只有 output |
| 典型用途 | `upload_video`、`transcode`、`send_email` | `get_status`、`list_quotas`、`current_config` |

**判斷原則**:同樣的 args call 兩次會產生額外的 state 變化嗎?會就是 tool。只是重複 read 就是 resource。

## Platform 怎麼 aggregate

```
     ┌──────────────────────────────────────────────────┐
     │       Platform MCP aggregator(一個 endpoint)    │
     │   expose: video.transcode, user.create,          │
     │           billing.charge, oauth.sign_in, …       │
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

1. Agent call `video.transcode` 給 aggregator
2. Aggregator 查 `video` module 的 endpoint,加上 Internal auth
3. Forward 到 module 的 `/__mirrorstack/mcp/tools/call`
4. Module handler 拿 aggregator 附上的 user identity 跑

## Trust boundary

三層,每次 hop 一層:

1. **Agent → Aggregator** — 對 aggregator 證明 user identity(session、API key、host 用什麼認都行)
2. **Aggregator → Module** — Internal scope auth(`X-MS-Internal-Secret`)。Module 信任 aggregator 已經驗過 identity。
3. **Module → DB / storage** — per-app credential,scope 限制在 agent 那個 request 屬於的 app。

**Module handler 絕對不要信任 agent 自己 claim 的 identity field**(像 args 裡面有 `userId` 這種)。要拿 identity 用 `auth.Get(ctx)`,那才是 aggregator 驗過的。

## Design principles

- **小而專一的 tool**。`upload_video` 贏過 `do_video_stuff`。Agent 組合小 tool 的能力遠比操作 monolithic tool 好。
- **Typed schemas 配 description**。用 `jsonschema:"description=..."` struct tag — agent 會把它當 documentation 讀。
- **盡量 idempotent**。網路 flaky 的時候,`create_or_update` 比 `create` + `update` 好用。
- **Error message 要講清楚**。`"quota exceeded — try again after 2025-01-15"` 贏過 `"error 503"`。Agent 會讀 error string 然後調整。
- **沒 output 也 OK**。有些 tool 就是做完事情就結束。Return `struct{}{}` 或最小的 ack。
- **Expose human 看得到的東西**。Dashboard 有的 resource,大概也應該是 MCPResource。Agent 不應該比 UI 是二等公民。

## 完整範例

一個 video module expose:

```go
// Tool — 有 side effect
type TranscodeArgs struct {
    VideoID string `json:"videoId" jsonschema:"description=videos table 的 ID"`
    Preset  string `json:"preset"  jsonschema:"description=hd、sd、或 audio_only"`
}
type TranscodeResult struct {
    JobID string `json:"jobId"`
}
ms.MCPTool("transcode", "把一個 video queue 起來做 transcode",
    func(ctx context.Context, a TranscodeArgs) (TranscodeResult, error) {
        jobID, err := queueTranscode(ctx, a.VideoID, a.Preset)
        return TranscodeResult{JobID: jobID}, err
    })

// Resource — read-only
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

Agent 收到 "把我最新的 video transcode 成 HD" 的 prompt 會:

1. Call `video.search({q: "latest"})` — 找到 ID
2. Call `video.transcode({videoId, preset: "hd"})` — queue 起來
3. Read `video.transcode_status` 直到 State 變 `done`

每一步都可以從 manifest 的 tool/resource description 看出來。Agent 不需要事先知道你 module 長什麼樣子,descriptions 就夠了。

---

## 相關文件

- [API reference — Agent surface (MCP)](../api-reference.md) — Function signature 跟用法。
- [Manifest](./manifest.md) — MCP surface 在 manifest payload 裡面長什麼樣。
- [Scopes](./scopes.md) — 為什麼 MCP routes 要放 Internal scope。
