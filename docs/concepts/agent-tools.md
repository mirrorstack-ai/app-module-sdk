# Agent tools

> Language: **English** · [繁體中文](../zh-TW/concepts/agent-tools.md)

MirrorStack positions as an **Agentic CMS** — every module must be operable by an AI agent, not just a human UI. Exposing at least one agent-callable tool or resource is a baseline requirement, which is why `mcp.go` is always present in the template and never gated behind a CLI flag.

This doc explains the three patterns for agent access, how to pick between them, and the design principles that separate good agent tools from bad ones.

## The three patterns

### 1. MCP tools + resources (baseline — every module ships these)

The primary mechanism. `ms.MCPTool` registers an agent-callable operation; `ms.MCPResource` registers an agent-readable snapshot. The SDK serves them under Internal scope at `/__mirrorstack/mcp/*` and publishes their schemas in the manifest.

The platform's MCP aggregator reads every installed module's manifest and exposes **one unified MCP server** to agents, where each tool is namespaced `{module-id}.{tool-name}`. Any MCP-speaking client (Claude Code, Cursor, ChatGPT Desktop, custom agent runtimes) can connect.

**Use MCP when:**
- The action or query is discoverable at install time (the schema is stable)
- The agent picks WHICH tool to call based on the description
- You want portability across agent hosts

### 2. Claude Code skills (workflow orchestration)

Skills are Claude-Code-specific markdown files that bundle multi-step workflows under a single slash command. A skill doesn't run code — it tells Claude how to orchestrate other primitives (usually MCP tools).

**Use skills when:**
- A workflow involves 3+ steps that always happen together
- The steps need LLM judgment calls (which preset, which user, etc.)
- You're shipping a Claude Code plugin with opinionated defaults

Skills call MCP tools underneath. Skills are DX sugar; MCP is the protocol.

### 3. Subagents (isolated task execution)

Claude Code supports subagents — LLM instances with their own context window and a scoped tool allowlist. Useful when the main agent needs to delegate without polluting its context.

**Use subagents when:**
- A task is long-running or produces a lot of intermediate output
- The work benefits from a cheaper/faster model (Haiku for batch processing)
- You need guardrails (the subagent only sees a subset of tools)

Subagents also call MCP tools — just with a narrower allowlist.

### Picking between them

```
Question                                   Answer
---------------------------------------------------------------------
Does my module expose functionality?       → Ship MCP tools/resources.
                                             (Required baseline.)

Do I ship a Claude Code plugin too?        → Consider skills for the
                                             common workflows users
                                             hit repeatedly.

Is one flow long/noisy/cost-sensitive?     → Wrap it in a subagent
                                             with a narrow tool list.
```

MCP is always the foundation. Skills and subagents are opt-in layers on top.

## Tool vs Resource

| | Tool | Resource |
|---|---|---|
| Side effects | Yes | No — read-only snapshot |
| Input args | Arbitrary struct (JSON Schema) | None |
| Schema exposed | Input + output | Output only |
| Typical use | `upload_video`, `transcode`, `send_email` | `get_status`, `list_quotas`, `current_config` |

**Rule of thumb**: if calling it twice with the same args should produce additional state changes, it's a tool. If it's a repeatable read, it's a resource.

## How the platform aggregates

```
     ┌──────────────────────────────────────────────────┐
     │       Platform MCP aggregator (one endpoint)     │
     │   exposes: video.transcode, user.create,         │
     │            billing.charge, oauth.sign_in, …      │
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

1. Agent calls `video.transcode` on the aggregator
2. Aggregator looks up the `video` module's endpoint, adds Internal auth
3. Forwards to `/__mirrorstack/mcp/tools/call` on the module
4. Module's handler runs with user identity from the aggregator-added headers

## Trust boundary

Three layers, one per hop:

1. **Agent → Aggregator** — user identity proven to the aggregator (session, API key, whatever the host uses)
2. **Aggregator → Module** — Internal scope auth (`X-MS-Internal-Secret`). The module trusts the aggregator to have verified identity.
3. **Module → DB / storage** — per-app credentials scoped to the app the agent's request belongs to.

**Module handlers NEVER trust agent-claimed identity fields** (like a `userId` in the args). Use `auth.Get(ctx)` for the identity the aggregator verified.

## Design principles

- **Small, focused tools.** `upload_video` beats `do_video_stuff`. Agents compose small tools better than they operate on monolithic ones.
- **Typed schemas with descriptions.** Use `jsonschema:"description=..."` struct tags — agents read them as documentation.
- **Idempotency where possible.** `create_or_update` beats `create` + `update` for flaky network conditions.
- **Errors that explain.** `"quota exceeded — try again after 2025-01-15"` beats `"error 503"`. Agents read error strings and adjust.
- **No output is fine.** Some tools just do their thing. Return `struct{}{}` or a minimal ack.
- **Expose what humans can see.** If a resource is in the dashboard, it should probably also be an MCPResource. Agents shouldn't be second-class to the UI.

## Worked example

A video module exposes:

```go
// Tool — side-effecting
type TranscodeArgs struct {
    VideoID string `json:"videoId" jsonschema:"description=ID from the videos table"`
    Preset  string `json:"preset"  jsonschema:"description=hd, sd, or audio_only"`
}
type TranscodeResult struct {
    JobID string `json:"jobId"`
}
ms.MCPTool("transcode", "Queue a video for transcoding",
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
ms.MCPResource("transcode_status", "Current transcode job statuses",
    func(ctx context.Context) ([]TranscodeStatus, error) {
        return listActiveJobs(ctx)
    })
```

An agent prompt "transcode my latest video to HD" would:

1. Call `video.search({q: "latest"})` — find the ID
2. Call `video.transcode({videoId, preset: "hd"})` — queue the job
3. Read `video.transcode_status` until State = `done`

Each step is discoverable from the manifest. The agent needs no prior knowledge of your module beyond the descriptions.

---

## Related

- [API reference — Agent surface (MCP)](../api-reference.md) — function signatures and usage.
- [Manifest](./manifest.md) — how the MCP surface appears in the manifest payload.
- [Scopes](./scopes.md) — why MCP routes live under Internal scope.
