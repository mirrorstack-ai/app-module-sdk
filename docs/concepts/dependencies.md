# Dependencies

> Language: **English** · [繁體中文](../zh-TW/concepts/dependencies.md)

A MirrorStack module declares dependencies on other modules by their ID, not by abstract capability names. The platform's catalog uses these declarations to drive install ordering (required deps) and to let modules opportunistically integrate (optional deps).

## Required vs optional

| | Required | Optional |
|---|---|---|
| **Declared with** | `ms.DependsOn(spec)` at root | `ms.Needs(spec, handler)` wrapping a handler |
| **Install behavior** | Catalog installs the dep first; install fails if the dep is missing or no published version matches the constraint. | Catalog ignores; your module installs standalone. |
| **Uninstall behavior** | The dep cannot be uninstalled while your module is installed. | The dep can be uninstalled anytime; your module keeps running. |
| **Runtime guarantee** | Always present. | Must check with `ms.Resolve[T](id)` before use. |
| **Manifest shape** | `{"id":"oauth-core","version":"^1.2.0"}` | `{"id":"video","version":"^1","optional":true}` |

## Version constraints

Both `DependsOn` and `Needs` accept a spec of the form `"id"` (any version) or `"id@constraint"`. Constraints use npm-style SemVer syntax, validated at registration time — an invalid constraint panics immediately.

| Spec | Accepts |
|---|---|
| `"oauth-core"` | Any version |
| `"oauth-core@^1.2.0"` | `>=1.2.0, <2.0.0` — compatible within major |
| `"oauth-core@~1.2.0"` | `>=1.2.0, <1.3.0` — compatible within minor |
| `"oauth-core@1.x"` | Any `1.x.x` |
| `"oauth-core@>=1.2.0 <2.0.0"` | Explicit range |
| `"oauth-core@1.2.3"` | Exact |

The catalog enforces constraints at install time. Missing constraint = module author takes what they get; present constraint = catalog rejects incompatible versions.

## Required: `ms.DependsOn`

Declared once at module init. The module cannot start without the dep installed.

```go
func main() {
    ms.Init(...)
    ms.DependsOn("oauth-core@^1.2.0")   // required, any 1.2.x through 1.x
    ms.Start()
}
```

`ms.DependsOn` always registers as required — no positional magic. Put it in `main()` or a package-level `init()`; there's no semantic difference.

## Optional: `ms.Needs` wraps the handler

Optional deps are declared at the **handler registration site** — co-located with the code that uses them. `ms.Needs(id, handler)` registers the dep as optional and returns the handler unchanged, so it composes with any API that takes an `http.HandlerFunc`.

```go
func main() {
    ms.Init(...)
    ms.DependsOn("oauth-core@^1")                                             // required
    ms.OnEvent("video.completed", ms.Needs("video@^1", onVideoCompleted))     // optional
    ms.Cron("cleanup", "0 3 * * *", ms.Needs("storage@^2.1", runCleanup))     // optional
    ms.Start()
}
```

**Why this shape**:

1. The dep and the handler that uses it are literally on the same line — no "what is this setup function for again" dance.
2. No positional auto-detect rule. `ms.DependsOn` has one unambiguous meaning. Extract-function refactors can't silently change classification.
3. Works uniformly across `OnEvent`, `Cron`, chi routes, and anything else that accepts a handler.

### Multiple optional deps

Nest `Needs`:

```go
ms.OnEvent("payment", ms.Needs("billing", ms.Needs("audit-log", onPayment)))
```

Each `Needs` call registers one dep; the outermost returns the final wrapped handler.

### Chi route example

```go
ms.Platform(func(r chi.Router) {
    r.Get("/transcode", ms.Needs("video", transcodeHandler))
    r.Post("/ship",     ms.Needs("billing", ms.Needs("shipping", shipHandler)))
})
```

## Dedup rule

If the same dependency is declared multiple times across the codebase, **required wins**:

| First declaration | Second declaration | Stored |
|---|---|---|
| required (`DependsOn`) | required | required |
| required (`DependsOn`) | optional (`Needs`) | required |
| optional (`Needs`) | required (`DependsOn`) | required (upgraded) |
| optional (`Needs`) | optional (`Needs`) | optional |

A second optional declaration is a no-op; a required declaration upgrades a prior optional entry. This means it's safe to sprinkle `Needs` freely — if the dep is already required elsewhere, `Needs` degrades gracefully.

## Runtime use: `ms.Resolve[T]`

Inside a handler wrapped by `ms.Needs`, check whether the dep is actually present before using it:

```go
func onVideoCompleted(w http.ResponseWriter, r *http.Request) {
    if video, ok := ms.Resolve[videoclient.Client]("video"); ok {
        // video module is installed — use it
        video.MarkProcessed(r.Context(), videoID)
    }
    // if not installed, skip gracefully
}
```

**v1 note**: cross-module client wiring is not yet implemented. `Resolve[T]` currently always returns `(zero, false)`. The API shape is committed so code written today keeps working when real resolution lands.

## Versioning

No version constraints on `DependsOn` today. Breaking changes are handled by bumping the module ID (e.g. `oauth-core` → `oauth-core-v2`), the same pattern Go stdlib uses for `database/sql` vs `database/sql/v2`.

Semver range support (`>=1.0.0 <2.0.0`) is deferred until the catalog grows enough to justify a constraint solver.
