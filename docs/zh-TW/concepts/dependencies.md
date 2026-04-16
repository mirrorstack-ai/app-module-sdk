# Dependencies

> Language: [English](../../concepts/dependencies.md) · **繁體中文**

MirrorStack module 用 **module ID** 來 declare 對其他 module 的 dependency,不是用什麼抽象的 capability name。Platform 的 catalog 拿這些 declaration 來決定 install 順序(required deps),還有讓 module 之間有機會 integrate(optional deps)。

## Required vs optional

| | Required | Optional |
|---|---|---|
| **Install 行為** | Catalog 會先裝好 dep;dep 不在就 install 失敗。 | Catalog 不管;你 module 可以 standalone 裝。 |
| **Uninstall 行為** | 你 module 還裝著的話,dep 不能被 uninstall。 | Dep 隨時可以 uninstall;你 module 照跑。 |
| **Runtime 保證** | 一定在。 | 要用 `ms.Resolve[T](id)` 先檢查。 |
| **Manifest shape** | `{"id":"oauth-core"}` | `{"id":"user","optional":true}` |

## Auto-detection 規則

只有**一個** `ms.DependsOn(id)` function。Required vs optional 是從「你在哪裡 call 它」自動判斷的:

| Caller | 歸類成 |
|---|---|
| `func main()`(package `main` 裡面) | Required |
| Package-level `init()` function(`pkg.init` 或 `pkg.init.N`) | Required |
| 其他地方(helpers、handlers、setup functions 等) | Optional |

做法:SDK 用 `runtime.Callers` walk Go 的 call stack,第一個不是 SDK frame 的 function name 去比對 `main.main` 或 `*.init[.N]`。

```go
func main() {
    ms.Init(...)
    ms.DependsOn("oauth-core")   // REQUIRED — caller 是 main.main

    configureVideoFallback()

    ms.Start()
}

func configureVideoFallback() {
    ms.DependsOn("video")        // OPTIONAL — caller 是 helper function
    ms.OnEvent("video.completed", onVideoCompleted)
}
```

## Extract-function 的坑

Auto-detect 的規則是看位置的。如果你把 `ms.DependsOn(...)` 從 `main()` refactor 出去變成 helper function,它的分類會**悄悄地**從 required 變成 optional。

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
    registerDeps()                 // 看起來沒什麼大不了的 refactor...
    ms.Start()
}

func registerDeps() {
    ms.DependsOn("oauth-core")     // 現在變成 OPTIONAL 了 — 悄悄地!
    ms.DependsOn("user")           // 現在變成 OPTIONAL 了 — 悄悄地!
}
```

### 為什麼要這樣 trade-off

有考慮過要不要用顯式的方式(加 flag argument、或用第二個 function name)然後都 reject 了。代價 — 你要記住這條規則 — 比每次 declare 都要多寫 ceremony 要來得小。這個 auto-detect 規則跟 `testing.T.Parallel` 是一樣的概念:位置本身就是資訊。

### Escape hatch

如果你真的很需要在 `main()` 外面 declare required deps — 比如說你有一個共用的 registration package — 用 package-level 的 `init()`。`init()` 會被歸類成 required:

```go
// deps.go — 獨立的 registration package
package deps

import ms "github.com/mirrorstack-ai/app-module-sdk"

func init() {
    ms.DependsOn("oauth-core")   // REQUIRED — caller 是 pkg.init
    ms.DependsOn("user")         // REQUIRED — caller 是 pkg.init
}
```

`init()` 會比 `main()` 早跑,而 `ms.Init` 之後 call `ms.DependsOn` 是安全的。import 的順序要 arrange 好,讓 `ms.Init` 先跑。

## Dedup 規則

同一個 dependency 在 codebase 裡面 declare 很多次的話,**required 贏**:

| 第一次 declare | 第二次 declare | 存起來的是 |
|---|---|---|
| required | required | required |
| required | optional | required |
| optional | required | required(升級) |
| optional | optional | optional |

第二次 call optional 的話就 no-op;第二次 call required 會把之前的 optional 升級成 required。

## Runtime 用:`ms.Resolve[T]`

Optional deps 要在 runtime 檢查有沒有:

```go
if user, ok := ms.Resolve[userclient.Client]("user"); ok {
    // user module 有裝 — 直接 call
    uid, _ := user.UpsertByExternalIdentity(ctx, ext)
    issueSession(uid)
} else {
    // Fallback — 用 platform 原生的 identity resolution
    uid, _ := platform.ResolveIdentity(ctx, ext)
    issueSession(uid)
}
```

**v1 note**:Cross-module client wiring 還沒做。`Resolve[T]` 現在永遠 return `(zero, false)`。API shape 已經 commit 了,所以今天寫的 code 之後 real resolution 上線時會自動 work。

## Versioning

`DependsOn` 目前沒有 version constraint。Breaking change 的處理方式是 bump module ID(例如 `oauth-core` → `oauth-core-v2`),跟 Go stdlib 的 `database/sql` vs `database/sql/v2` 同一套 pattern。

Semver range 的 support(`>=1.0.0 <2.0.0`)等 catalog 大到需要 constraint solver 再說。
