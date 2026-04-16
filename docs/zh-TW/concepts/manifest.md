# Manifest

> Language: [English](../../concepts/manifest.md) · **繁體中文**

每個 MirrorStack module 都會在 Internal scope 下提供 `GET /__mirrorstack/platform/manifest`。平台 catalog 在 deploy 時讀這個 endpoint,取得 module 的身分、routes、events、permissions、agent surface 等資訊。

Manifest 是**向後相容、只追加的** wire contract — 新欄位採用 `omitempty`(或固定輸出空陣列),舊版 catalog consumer 照樣能解析。

## 完整 wire shape

```json
{
  "id": "video",
  "defaults": {
    "name": "Video",
    "icon": "videocam"
  },
  "description": "HLS video streaming 與 transcoding。",
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
        "description": "依標題搜尋 videos",
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

## 欄位對照

| 欄位 | 來源 | 永遠存在 |
|---|---|---|
| `id` | `Config.ID` | 是 |
| `defaults.name` / `defaults.icon` | `Config.Name` / `Config.Icon` | 是 |
| `description` | `ms.Describe(...)` | 否 — 空值會省略 |
| `dependencies` | `ms.DependsOn(...)` | 是(沒有時輸出 `[]`) |
| `migration.app` | `sql/app/` 下最新的檔案 | 是(SQL 未設定時為 `""`) |
| `migration.module` | `sql/module/` 下最新的檔案 | 否 — `omitempty` |
| `versions` | `Config.Versions` | 是(nil 時輸出 `{}`) |
| `routes.{platform,public,internal}` | Route 註冊 | 是(每個 scope 空時輸出 `[]`) |
| `events.emits` / `events.subscribes` | `ms.Emits` / `ms.OnEvent` | 是 |
| `schedules` | `ms.Cron` | 是 |
| `tasks` | `ms.OnTask` | 是 |
| `permissions` | `ms.RequirePermission` | 是 |
| `mcp.tools` / `mcp.resources` | `ms.MCPTool` / `ms.MCPResource` | 是(空時輸出 `[]`) |

## Catalog 收到之後會做什麼

- **`id`、`description`** — 探索:agent 決定要安裝哪個 module 時,讀的是精簡的 `{id, description}` 索引。
- **`dependencies`** — 安裝規劃器做拓撲排序,required 相依先裝。
- **`migration`、`versions`** — 把 semver 部署狀態對應到每個 scope 的 migration 編號,升級 / 降級時使用。
- **`routes`** — Catalog 看到的「此 module 提供哪些 endpoint」,與實際在 production 掛載的位置無關。
- **`events`** — Event-bus 連線設定;平台把發出的事件路由到 subscriber 的 path。
- **`schedules`** — 平台排程器據此建立 cron trigger。
- **`tasks`** — 部署 pipeline 據此建立 SQS queue 與 ECS task definition。
- **`permissions`** — 安裝畫面會顯示「這個 module 需要這些權限」。
- **`mcp`** — 聚合後的 MCP server 把這些 tools 與 resources 納入,供 agent 呼叫。

## 如何讀取 manifest

```bash
curl -sH "X-MS-Internal-Secret: $MS_INTERNAL_SECRET" \
     http://localhost:8080/__mirrorstack/platform/manifest | jq
```

Production 上,平台會在每次 deploy 時讀一次並快取。
