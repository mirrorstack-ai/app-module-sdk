# Manifest

> Language: [English](../../concepts/manifest.md) · **繁體中文**

## 資料表命名規則

模組的 `app_<id>` schema 內所有資料表名稱**必須**以模組 ID 加底線開頭：`<module_id>_<table_name>`。這是為了防止多個模組共用同一 schema 時發生名稱衝突。範例：模組 `media` 建立 `media_items`、`media_tags` — 不可以只叫 `items`。

---

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
- **`tasks`** — 宣告 managed task handler、重試政策與執行資源。
- **`permissions`** — 安裝畫面會顯示「這個 module 需要這些權限」。
- **`mcp`** — 聚合後的 MCP server 把這些 tools 與 resources 納入,供 agent 呼叫。

## 如何讀取 manifest

```bash
curl -sH "X-MS-Internal-Secret: $MS_INTERNAL_SECRET" \
     http://localhost:8080/__mirrorstack/platform/manifest | jq
```

Production 上,平台會在每次 deploy 時讀一次並快取。

## Release manifest tool mode

MirrorStack CLI 會使用 SDK 保留的 process mode，從與 Linux artifact 相同的
凍結 source tree 建立 release evidence。Module code 不應直接呼叫此模式。
CLI 會設定：

```text
MS_SDK_TOOL_MODE=release-manifest-v1
```

所有 startup declaration 完成後，`ms.Start()` 會從 stdin 讀取一個 JSON
object（上限 4 KiB），並要求後面必須是 EOF：

```json
{"source_sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
```

`source_sha256` 必須剛好是 64 個小寫十六進位字元。缺少欄位、未知欄位、
重複欄位、錯誤型別或 trailing input 都會 fail closed。未知且非空的
`MS_SDK_TOOL_MODE` 也會 fail closed，不會繼續啟動 module。

成功時 stdout 只會輸出一行 JSON：

```json
{"protocol":"mirrorstack.release-manifest/v1","source_sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","manifest_sha256":"<64 lowercase hex>","manifest_base64":"<standard padded base64>"}
```

`manifest_base64` 解碼後就是一般 manifest handler 提供的完全相同 bytes，
包含 `json.Encoder` 結尾的 newline。CLI 會先解碼、重新計算
`manifest_sha256`，驗證成功後才解析 manifest；它不會重新序列化 JSON，
也不會假設其他語言能產生相同 bytes。Caller 提供的 source hash 只屬於
release metadata，不會改變一般提供的 manifest。

這個 tool path 會在 development migration 或 database setup、Lambda handoff、
one-shot task polling 與 HTTP listening 之前返回。SDK diagnostics 只寫到
stderr。Stdout 保留給 envelope；任何額外的 module-authored stdout 都會讓
CLI 拒絕該 candidate。
