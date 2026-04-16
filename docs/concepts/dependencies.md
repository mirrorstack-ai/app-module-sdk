# Dependencies

> Language: **English** · [繁體中文](../zh-TW/concepts/dependencies.md)

A MirrorStack module declares dependencies on other modules by their ID, not by abstract capability names. The platform's catalog uses these declarations to drive install ordering (required deps) and to let modules opportunistically integrate (optional deps).

## Required vs optional

| | Required | Optional |
|---|---|---|
| **Install behavior** | Catalog installs the dep first; install fails if the dep is missing. | Catalog ignores; your module installs standalone. |
| **Uninstall behavior** | The dep cannot be uninstalled while your module is installed. | The dep can be uninstalled anytime; your module keeps running. |
| **Runtime guarantee** | Always present. | Must check with `ms.Resolve[T](id)` before use. |
| **Manifest shape** | `{"id":"oauth-core"}` | `{"id":"user","optional":true}` |

## Auto-detection rule

There is **one** `ms.DependsOn(id)` function. Required vs optional is auto-detected from where you call it:

| Caller | Classification |
|---|---|
| `func main()` in package `main` | Required |
| Any package-level `init()` function (`pkg.init` or `pkg.init.N`) | Required |
| Anywhere else (helpers, handlers, setup functions) | Optional |

Implementation: the SDK walks the Go call stack with `runtime.Callers` and matches the first non-SDK frame's function name against `main.main` or `*.init[.N]`.

```go
func main() {
    ms.Init(...)
    ms.DependsOn("oauth-core")   // REQUIRED — caller is main.main

    configureVideoFallback()

    ms.Start()
}

func configureVideoFallback() {
    ms.DependsOn("video")        // OPTIONAL — caller is helper func
    ms.OnEvent("video.completed", onVideoCompleted)
}
```

## The extract-function caveat

The auto-detect rule is positional. If you move a `ms.DependsOn(...)` call out of `main()` into a helper, its classification silently changes from required to optional.

```go
// BEFORE refactor
func main() {
    ms.Init(...)
    ms.DependsOn("oauth-core")     // required
    ms.DependsOn("user")           // required
    ms.Start()
}

// AFTER "extract function" refactor
func main() {
    ms.Init(...)
    registerDeps()                 // innocuous refactor...
    ms.Start()
}

func registerDeps() {
    ms.DependsOn("oauth-core")     // NOW OPTIONAL — silently!
    ms.DependsOn("user")           // NOW OPTIONAL — silently!
}
```

### Why this tradeoff

Explicit alternatives (a flag argument, a second function name) were considered and rejected. The cost — one subtle rule module authors must remember — is lower than the cost of adding ceremony every declaration. The auto-detect rule mirrors how `testing.T.Parallel` works: position in the source is meaningful.

### Escape hatch

If you genuinely need required deps outside `main()` — e.g. a shared registration package — use a package-level `init()` function. `init()` is classified as required:

```go
// deps.go — separate registration package
package deps

import ms "github.com/mirrorstack-ai/app-module-sdk"

func init() {
    ms.DependsOn("oauth-core")   // REQUIRED — caller is pkg.init
    ms.DependsOn("user")         // REQUIRED — caller is pkg.init
}
```

`init()` runs before `main()`, and `ms.DependsOn` is safe to call after `ms.Init`. Order your imports so `ms.Init` runs first.

## Dedup rule

If the same dependency is declared multiple times across the codebase, **required wins**:

| First declaration | Second declaration | Stored |
|---|---|---|
| required | required | required |
| required | optional | required |
| optional | required | required (upgraded) |
| optional | optional | optional |

The second optional call is a no-op; the second required call upgrades a prior optional entry.

## Runtime use: `ms.Resolve[T]`

For optional deps, check presence at runtime:

```go
if user, ok := ms.Resolve[userclient.Client]("user"); ok {
    // user module is installed — use it
    uid, _ := user.UpsertByExternalIdentity(ctx, ext)
    issueSession(uid)
} else {
    // fallback — platform-native identity resolution
    uid, _ := platform.ResolveIdentity(ctx, ext)
    issueSession(uid)
}
```

**v1 note**: cross-module client wiring is not yet implemented. `Resolve[T]` currently always returns `(zero, false)`. The API shape is committed so code written today keeps working when real resolution lands.

## Versioning

No version constraints on `DependsOn` today. Breaking changes are handled by bumping the module ID (e.g. `oauth-core` → `oauth-core-v2`), the same pattern Go stdlib uses for `database/sql` vs `database/sql/v2`.

Semver range support (`>=1.0.0 <2.0.0`) is deferred until the catalog grows enough to justify a constraint solver.
