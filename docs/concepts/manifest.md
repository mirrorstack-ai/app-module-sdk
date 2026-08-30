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
- **`tasks`** — declares managed task handlers, retry policy, and execution resources.
- **`permissions`** — install screen shows "this module needs these permissions."
- **`mcp`** — aggregated MCP server ingests tools + resources so agents can call them.

## Fetching the manifest

```bash
curl -sH "X-MS-Internal-Secret: $MS_INTERNAL_SECRET" \
     http://localhost:8080/__mirrorstack/platform/manifest | jq
```

In production the platform fetches this at every deploy and caches the result.

## Release manifest tool mode

The MirrorStack CLI uses a reserved SDK process mode to build release evidence
from the same frozen source tree as the Linux artifact. Module code should not
call this mode directly. The CLI sets:

```text
MS_SDK_TOOL_MODE=release-manifest-v1
```

After all startup declarations have run, `ms.Start()` reads one JSON object
from stdin (bounded to 4 KiB) and requires EOF:

```json
{"source_sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
```

`source_sha256` must be exactly 64 lowercase hexadecimal characters. Missing,
unknown, duplicate, wrongly typed, or trailing input fails closed. An unknown
non-empty `MS_SDK_TOOL_MODE` also fails closed instead of starting the module.

Success writes exactly one JSON line to stdout:

```json
{"protocol":"mirrorstack.release-manifest/v1","source_sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","manifest_sha256":"<64 lowercase hex>","manifest_base64":"<standard padded base64>"}
```

`manifest_base64` decodes to the exact bytes served by the ordinary manifest
handler, including `json.Encoder`'s trailing newline. The CLI decodes those
bytes, recomputes `manifest_sha256`, and only then parses the manifest. It does
not reserialize JSON and assume another language will produce the same bytes.
The caller-provided source hash is release metadata only; it does not change
the ordinary served manifest.

This tool path returns before development migrations or database setup, Lambda
handoff, one-shot task polling, and HTTP listening. SDK diagnostics stay on
stderr. Stdout is reserved for the envelope; any other module-authored stdout
causes the CLI to reject the candidate.
