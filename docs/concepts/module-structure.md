# Module structure

> Language: **English** · [繁體中文](../zh-TW/concepts/module-structure.md)

Every shipped MirrorStack module has the same skeleton. **All 11 of them
follow it**, and until now it was written down in exactly one place — one
module's own `CLAUDE.md` — so a new author had to reverse-engineer it from a
neighbour, and `mirrorstack module init` scaffolded something else entirely.
This page is the rule.

## The boundary

There is one question, and it decides which directory a file goes in:

> **Does this code mount a route or carry an `http.HandlerFunc`?**
>
> - **No** — it is pure-data declaration → `declare/`
> - **Yes** — it runs per request → `internal/handlers/`

`declare/` mutates the SDK registry once, at startup, before `ms.Start()`.
`internal/handlers/` answers requests, forever, after it. Nothing in
`internal/handlers/` should call `ms.RegisterPermission` or `ms.ContributesTo`.

**One exception, and it is a real one.** `ms.OnEvent` and `ms.Cron` take a
handler with an HTTP shape, and every module that uses them keeps that handler
in `declare/` beside its subscription. Moving it to `internal/handlers/` would
need `declare/` to import `handlers`, which already imports `declare` — so the
honest options are an import cycle or a third package, and the fleet settled on
keeping them together. What `declare/` must never do is **mount a route**:
`ms.Public` / `ms.Platform` / `ms.Internal` belong in `internal/handlers/`, and
`scripts/verify-module.sh` enforces exactly that.

That split is not aesthetic. The declaration set becomes the **manifest**, and
the manifest is what the platform reads to decide what a module may do —
install-time preconditions, permission defaults, exposed tables, contribution
slots. Keeping it in files that cannot see a request is what makes it a
statement about the module rather than about one caller.

## The tree

```
my-module/
├── main.go                     ms.Init(Config) → RegisterMessages → declare.Register()
│                               → handlers.Register() → ms.Start()
├── go.mod
├── CHANGELOG.md                one `## <version>` section per release; HARD-LINTED at deploy
├── declare/
│   ├── register.go             Register(): the one entry point main.go calls
│   ├── permissions.go          ms.RegisterPermission — names, default roles
│   ├── ui.go                   ms.RegisterUI — nav entries, pages
│   ├── events.go               ms.Emits / ms.OnEvent declarations
│   ├── expose.go               ms.ExposeTable — the read-only surface for dependents
│   ├── contracts.go            shared refs + payload types for ms.ContributesTo
│   └── meter.go                ms.RegisterMetric
├── internal/
│   ├── handlers/
│   │   ├── routes.go           Register(): ms.Public / ms.Platform / ms.Internal blocks
│   │   ├── mcp.go              ms.MCPTool declarations and their handlers
│   │   └── <feature>.go        one file per feature, named for what it serves
│   └── sqlc/                   generated queries (sqlc.yaml at the module root)
├── i18n/
│   ├── en-US.json              permission labels, nav copy, tool descriptions
│   └── zh-TW.json              keep aligned with en-US.json, key for key
├── sql/app/
│   └── 0001_init.up.sql        per-app migrations; NEVER renumber a released one
└── web/                        optional admin UI; Config.WebDir points at web/dist
```

Only `main.go`, `go.mod`, `CHANGELOG.md`, `declare/` and `sql/app/` are
required. Add the rest when the module needs them.

## Two rules that are not obvious

**`main.go` never grows.** It reads `Config`, loads i18n, calls the two
`Register()` functions, and starts. Adding a feature never edits it — that is
the whole reason `declare.Register()` and `handlers.Register()` exist as single
entry points instead of a pile of `init()` functions. An `init()`-based
registry looks tidier and costs you deterministic ordering: i18n catalogs must
load *before* permission labels resolve, and `init()` gives you no way to say
so.

**Never name a file `handlers.go`.** It is the name you reach for when you have
not decided what the code is, and it is how `user-core/internal/handlers/handlers.go`
reached 1038 lines holding sessions, admin, profile and metering at once. Name
the file for the surface it serves — `sessions.go`, `profile.go`,
`metering.go` — and the split you would eventually have to do is already done.

## See also

- [Scopes](./scopes.md) — what Platform / Public / Internal each mean
- [Manifest](./manifest.md) — what `declare/` actually produces
- [Agent tools](./agent-tools.md) — `internal/handlers/mcp.go` in detail
