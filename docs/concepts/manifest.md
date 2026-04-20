# Manifest

> Language: **English** · [繁體中文](../zh-TW/concepts/manifest.md)

## Table naming convention

Every table in a module's `app_<id>` schema **must** start with the module ID followed by an underscore: `<module_id>_<table_name>`. This prevents collisions when multiple modules share the same schema. Example: module `media` creates `media_items`, `media_tags` — never bare `items`.

---

Every MirrorStack module serves `GET /__mirrorstack/platform/manifest` under Internal scope. The platform catalog reads this at deploy time to discover the module's identity, routes, events, permissions, and agent surface.

The manifest is an **additive wire contract** — new fields land with `omitempty` (or as always-present empty arrays) so old catalog consumers keep parsing correctly.

## Complete wire shape

```json
{
  "id": "video",
  "defaults": {
    "name": "Video",
    "icon": "videocam"
  },
  "description": "HLS video streaming and transcoding.",
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
        "description": "Search videos by title",
        "inputSchema":  { "type": "object", "properties": {"q": {"type": "string"}} },
        "outputSchema": { "type": "object", "properties": {"items": {"type": "array"}} }
      }
    ],
    "resources": [
      {"name": "status", "description": "Module health"}
    ]
  }
}
```

## Field reference

| Field | Source | Always present |
|---|---|---|
| `id` | `Config.ID` | yes |
| `defaults.name` / `defaults.icon` | `Config.Name` / `Config.Icon` | yes |
| `description` | `ms.Describe(...)` | no — omitted when empty |
| `dependencies` | `ms.DependsOn(...)` | yes (`[]` when none) |
| `migration.app` | latest file in `sql/app/` | yes (`""` if SQL unset) |
| `migration.module` | latest file in `sql/module/` | no — `omitempty` |
| `versions` | `Config.Versions` | yes (`{}` when nil) |
| `routes.{platform,public,internal}` | route registration | yes (each scope `[]` when none) |
| `events.emits` / `events.subscribes` | `ms.Emits` / `ms.OnEvent` | yes |
| `schedules` | `ms.Cron` | yes |
| `tasks` | `ms.OnTask` | yes |
| `permissions` | `ms.RequirePermission` | yes |
| `mcp.tools` / `mcp.resources` | `ms.MCPTool` / `ms.MCPResource` | yes (`[]` when none) |

## What the catalog does with it

- **`id`, `description`** — discovery: agent reads the compact `{id, description}` index when deciding which module to install.
- **`dependencies`** — install planner runs a topological sort, installs required deps first.
- **`migration`, `versions`** — translates semver deploy state to per-scope migration numbers for upgrade/downgrade.
- **`routes`** — catalog view of "what endpoints this module exposes," independent of where they're mounted in production.
- **`events`** — event-bus wiring; platform routes emitted events to subscriber paths.
- **`schedules`** — platform scheduler provisions cron triggers.
- **`tasks`** — deploy pipeline provisions SQS queues and ECS task definitions.
- **`permissions`** — install screen shows "this module needs these permissions."
- **`mcp`** — aggregated MCP server ingests tools + resources so agents can call them.

## Fetching the manifest

```bash
curl -sH "X-MS-Internal-Secret: $MS_INTERNAL_SECRET" \
     http://localhost:8080/__mirrorstack/platform/manifest | jq
```

In production the platform fetches this at every deploy and caches the result.
