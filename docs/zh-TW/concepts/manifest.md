# Manifest

> Language: [English](../../concepts/manifest.md) · **繁體中文**

每個 MirrorStack module 都會在 Internal scope 下面 serve `GET /__mirrorstack/platform/manifest`。Platform catalog 在 deploy 的時候讀這個 endpoint,取得 module 的 identity、routes、events、permissions、agent surface 這些東西。

Manifest 是**加法 wire contract** — 新的 field 進來用 `omitempty`(或永遠存在的空 array),舊的 catalog consumer 照樣 parse 得動。

## 完整的 wire shape

```json
{
  "id": "video",
  "defaults": {
    "name": "Video",
    "icon": "videocam"
  },
  "description": "HLS video streaming 跟 transcoding。",
  "dependencies": [
    {"id": "oauth-core"},
    {"id": "user", "optional": true}
  ],
  "migration": {
    "app": "0012",
    "module": "0003"
  },
  "versions": {
    "v0.1.0": {"app": "0008"},
    "v0.2.0": {"app": "0012", "module": "0003"}
  },
  "routes": {
    "platform": [{"method": "POST", "path": "/videos"}],
    "public":   [{"method": "GET",  "path": "/stream/{id}"}],
    "internal": [{"method": "POST", "path": "/__mirrorstack/events/user.created"}]
  },
  "events": {
    "emits":      ["video.completed"],
    "subscribes": {"user.created": "/__mirrorstack/events/user.created"}
  },
  "schedules": [
    {"name": "cleanup", "cron": "0 3 * * *", "path": "/__mirrorstack/crons/cleanup"}
  ],
  "tasks": [
    {"name": "transcode", "maxDuration": "600s", "maxRetries": 3}
  ],
  "permissions": [
    {"name": "video.upload", "roles": ["admin", "member"]}
  ],
  "mcp": {
    "tools": [
      {
        "name": "search",
        "description": "用標題搜 videos",
        "inputSchema":  { "type": "object", "properties": {"q": {"type": "string"}} },
        "outputSchema": { "type": "object", "properties": {"items": {"type": "array"}} }
      }
    ],
    "resources": [
      {"name": "status", "description": "Module 健康狀態"}
    ]
  }
}
```

## Field reference

| Field | 來源 | 永遠都在 |
|---|---|---|
| `id` | `Config.ID` | 是 |
| `defaults.name` / `defaults.icon` | `Config.Name` / `Config.Icon` | 是 |
| `description` | `ms.Describe(...)` | 否 — 空的話會省略 |
| `dependencies` | `ms.DependsOn(...)` | 是(沒有就給 `[]`) |
| `migration.app` | `sql/app/` 裡面最新的 file | 是(SQL 沒設的話給 `""`) |
| `migration.module` | `sql/module/` 裡面最新的 file | 否 — `omitempty` |
| `versions` | `Config.Versions` | 是(nil 給 `{}`) |
| `routes.{platform,public,internal}` | Route registration | 是(每個 scope 空的話給 `[]`) |
| `events.emits` / `events.subscribes` | `ms.Emits` / `ms.OnEvent` | 是 |
| `schedules` | `ms.Cron` | 是 |
| `tasks` | `ms.OnTask` | 是 |
| `permissions` | `ms.RequirePermission` | 是 |
| `mcp.tools` / `mcp.resources` | `ms.MCPTool` / `ms.MCPResource` | 是(空的話給 `[]`) |

## Catalog 拿到之後做什麼

- **`id`, `description`** — discovery:agent 在決定要裝哪個 module 的時候,讀的是壓縮的 `{id, description}` index。
- **`dependencies`** — install planner 做 topological sort,required deps 先裝。
- **`migration`, `versions`** — 把 semver 的 deploy state 翻成 per-scope migration number,upgrade / downgrade 要用。
- **`routes`** — catalog 看到的「這個 module 提供哪些 endpoint」,跟 production 實際 mount 在哪裡無關。
- **`events`** — event-bus wiring;platform 把 emit 出來的 event 路由到 subscriber path。
- **`schedules`** — platform scheduler provision cron trigger。
- **`tasks`** — deploy pipeline provision SQS queue 跟 ECS task definition。
- **`permissions`** — install 畫面會顯示「這 module 需要這些 permission」。
- **`mcp`** — aggregated MCP server ingest 這些 tools + resources,讓 agent 可以 call。

## 怎麼抓 manifest

```bash
curl -sH "X-MS-Internal-Secret: $MS_INTERNAL_SECRET" \
     http://localhost:8080/__mirrorstack/platform/manifest | jq
```

Production 的話是 platform 在每次 deploy 都抓一次然後 cache 起來。
