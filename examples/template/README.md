# MirrorStack module template

A working module scaffold. Compiles as-is. The future `mirrorstack-cli` uses
this directory as the canonical source for `ms new <id>`.

## Layout

```
template/
├── main.go            # always — Init, Describe, DependsOn, Start
├── mcp.go             # always — agent tools + resources (required by design)
├── routes.go          # always — Platform/Public/Internal stubs
├── events.go          # --use-events
├── schedule.go        # --use-schedule
├── tasks.go           # --use-ecs
├── storage.go         # --use-storage
├── cache.go           # --use-cache
├── meter.go           # --use-meter
├── module_db.go       # --use-module-db
├── sql/app/
│   └── 0001_init.up.sql
├── go.mod
└── README.md
```

## How the hook pattern works

Each feature file registers itself through `postInitHooks`:

```go
// main.go (always present)
var postInitHooks []func()

func main() {
    ms.Init(ms.Config{...})
    ms.Describe("...")
    for _, h := range postInitHooks { h() }
    ms.Start()
}
```

```go
// schedule.go (optional)
func init() {
    postInitHooks = append(postInitHooks, registerSchedule)
}

func registerSchedule() {
    ms.Cron("cleanup", "0 3 * * *", cleanupHandler)
}
```

When the CLI scaffolds a new module, it copies `main.go` + `mcp.go` +
`routes.go` + the SQL + any feature files you opt into. `main.go` never needs
to change — dropped files simply stop registering themselves. Compiles with
any subset.

## Required deps vs optional deps

```go
func main() {
    ms.Init(...)
    ms.DependsOn("oauth-core")  // REQUIRED — called literally in main()

    configureWithUser()

    ms.Start()
}

func configureWithUser() {
    ms.DependsOn("user")        // OPTIONAL — auto-detected, called from helper
}
```

See [docs/concepts/dependencies.md](../../docs/concepts/dependencies.md) for the
full auto-detect rule and the extract-function caveat.

## Running locally

```bash
cd examples/template
MS_INTERNAL_SECRET=dev go run .
# Module listens on :8080
curl -sH "X-MS-Internal-Secret: dev" http://localhost:8080/__mirrorstack/platform/manifest | jq
```

The `replace` directive in `go.mod` points at the parent SDK so the template
stays in lockstep with local SDK changes. The CLI strips this directive when
copying into a user-facing repo.

## Removing a feature

Delete the file. That's it. No edits to `main.go`. `go build` still succeeds.

## Adding a feature

Add a new `<feature>.go` file with the same shape:

```go
package main

import ms "github.com/mirrorstack-ai/app-module-sdk"

func init() {
    postInitHooks = append(postInitHooks, registerMyFeature)
}

func registerMyFeature() {
    // Your ms.* registrations here.
}
```
