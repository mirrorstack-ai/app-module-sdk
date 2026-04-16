# Getting started

Build your first MirrorStack module in under 5 minutes.

## Prerequisites

- Go 1.26+
- A Postgres database for local development (optional — only needed if you use `ms.DB` / `ms.ModuleDB`)
- `MS_INTERNAL_SECRET` set to any string during dev (required for internal-scope routes)

## 1. Copy the template

```bash
cp -r path/to/app-module-sdk/examples/template ./my-module
cd my-module
```

Edit `main.go` — change the `Config.ID`, `Config.Name`, `Config.Icon`, and the `ms.Describe(...)` call to match your module.

## 2. Delete features you don't need

Drop any file whose feature you aren't using. No edits to `main.go` required; the template uses a hook pattern where each feature file self-registers.

| File | What it adds |
|---|---|
| `events.go` | `ms.OnEvent` + `ms.Emits` |
| `schedule.go` | `ms.Cron` |
| `tasks.go` | `ms.OnTask` (ECS task worker mode) |
| `storage.go` | `ms.Storage` (S3 + presigned multipart) |
| `cache.go` | `ms.Cache` (Redis) |
| `meter.go` | `ms.Meter` (billing events) |
| `module_db.go` | `ms.ModuleDB` / `ms.ModuleTx` (`mod_<id>` schema) |

`main.go`, `mcp.go`, and `routes.go` stay — agent-facing MCP tools and basic routes are required baseline.

## 3. Run it

```bash
MS_INTERNAL_SECRET=dev go run .
# template module (template) listening on :8080
```

Probe the manifest to confirm everything registered:

```bash
curl -sH "X-MS-Internal-Secret: dev" \
     http://localhost:8080/__mirrorstack/platform/manifest | jq
```

You should see your `description`, `dependencies`, `routes`, `mcp.tools`, etc.

## 4. Declare dependencies

In `main.go`, after `ms.Describe(...)`, add required deps:

```go
ms.DependsOn("oauth-core")   // required — catalog installs this first
```

For optional deps, declare them from a helper function (auto-detected):

```go
func configureWithUser() {
    ms.DependsOn("user")     // optional — module still runs if `user` missing
    ms.OnEvent("user.created", onUserCreated)
}
```

See [concepts/dependencies.md](./concepts/dependencies.md) for the full rule.

## 5. Register an agent tool

`mcp.go` already has a `greet` example. Replace with whatever your module should expose to AI agents:

```go
type SearchArgs struct {
    Query string `json:"query" jsonschema:"description=Free-text search"`
    Limit int    `json:"limit,omitempty"`
}
type SearchResult struct { Items []string `json:"items"` }

ms.MCPTool("search", "Search items by name",
    func(ctx context.Context, a SearchArgs) (SearchResult, error) {
        return SearchResult{Items: findItems(a.Query, a.Limit)}, nil
    })
```

JSON Schema is derived automatically from the `SearchArgs` and `SearchResult` struct fields.

## Next

- [API reference](./api-reference.md) — one-liner for every `ms.*` function.
- [Scopes](./concepts/scopes.md) — when to use Platform vs Public vs Internal.
- [Manifest](./concepts/manifest.md) — what the catalog sees from your module.
