# Dependencies

> Language: [English](../../concepts/dependencies.md) · **繁體中文**

MirrorStack module 用 **module ID** 來 declare 對其他 module 的 dependency,不是用什麼抽象的 capability name。Platform 的 catalog 拿這些 declaration 來決定 install 順序(required deps),還有讓 module 之間有機會 integrate(optional deps)。

## Required vs optional

| | Required | Optional |
|---|---|---|
| **Declare 方式** | 在 root 用 `ms.DependsOn(spec)` | 用 `ms.Needs(spec, handler)` 包住 handler |
| **Install 行為** | Catalog 會先裝好 dep;dep 不在、或沒有符合 constraint 的 version 就 install 失敗。 | Catalog 不管;你 module 可以 standalone 裝。 |
| **Uninstall 行為** | 你 module 還裝著的話,dep 不能被 uninstall。 | Dep 隨時可以 uninstall;你 module 照跑。 |
| **Runtime 保證** | 一定在。 | 要用 `ms.Resolve[T](id)` 先檢查。 |
| **Manifest shape** | `{"id":"oauth-core","version":"^1.2.0"}` | `{"id":"video","version":"^1","optional":true}` |

## Version constraints

`DependsOn` 跟 `Needs` 都接受 `"id"`(任何 version)或 `"id@constraint"` 這樣的 spec。Constraint 用 npm 風格的 SemVer 語法,註冊的時候就 validate — constraint 寫錯會立刻 panic。

| Spec | 接受的 version |
|---|---|
| `"oauth-core"` | 任何 version |
| `"oauth-core@^1.2.0"` | `>=1.2.0, <2.0.0` — major 內相容 |
| `"oauth-core@~1.2.0"` | `>=1.2.0, <1.3.0` — minor 內相容 |
| `"oauth-core@1.x"` | 任何 `1.x.x` |
| `"oauth-core@>=1.2.0 <2.0.0"` | Explicit range |
| `"oauth-core@1.2.3"` | Exact |

Catalog 在 install 時 enforce constraint。沒寫 constraint = 作者接受任何 version;有寫 = catalog 把不相容的版本拒掉。

## Required:`ms.DependsOn`

在 module init 的時候 declare 一次。沒裝這個 dep,module 起不來。

```go
func main() {
    ms.Init(...)
    ms.DependsOn("oauth-core@^1.2.0")   // required, 任何 1.2.x 到 1.x 都接受
    ms.Start()
}
```

`ms.DependsOn` 永遠 register 成 required — 沒有什麼位置判斷的魔法。放在 `main()` 或 package-level `init()` 都行,意思完全一樣。

## Optional:`ms.Needs` 包住 handler

Optional dep 是在**註冊 handler 的地方**一起 declare — 跟使用它的 code 放在同一行。`ms.Needs(id, handler)` 把 dep register 成 optional 然後把 handler 原樣 return,所以可以搭配任何吃 `http.HandlerFunc` 的 API。

```go
func main() {
    ms.Init(...)
    ms.DependsOn("oauth-core@^1")                                             // required
    ms.OnEvent("video.completed", ms.Needs("video@^1", onVideoCompleted))     // optional
    ms.Cron("cleanup", "0 3 * * *", ms.Needs("storage@^2.1", runCleanup))     // optional
    ms.Start()
}
```

**為什麼這樣設計:**

1. Dep 跟 handler 就在同一行 — 不用多繞一個「這個 setup function 到底是做什麼的」的彎路。
2. 沒有靠位置判斷的 auto-detect。`ms.DependsOn` 語意明確、只有一種解讀。Extract-function refactor 不會偷偷改掉分類。
3. 同一個 API 通用在 `OnEvent`、`Cron`、chi route、還有其他任何吃 handler 的地方。

### 一個 handler 要多個 optional dep

把 `Needs` 疊起來:

```go
ms.OnEvent("payment", ms.Needs("billing", ms.Needs("audit-log", onPayment)))
```

每一個 `Needs` call 註冊一個 dep;最外層那個 return 最終包好的 handler。

### Chi route 範例

```go
ms.Platform(func(r chi.Router) {
    r.Get("/transcode", ms.Needs("video", transcodeHandler))
    r.Post("/ship",     ms.Needs("billing", ms.Needs("shipping", shipHandler)))
})
```

## Dedup 規則

同一個 dependency 在 codebase 裡面 declare 很多次的話,**required 贏**:

| 第一次 declare | 第二次 declare | 存起來的是 |
|---|---|---|
| required(`DependsOn`) | required | required |
| required(`DependsOn`) | optional(`Needs`) | required |
| optional(`Needs`) | required(`DependsOn`) | required(升級) |
| optional(`Needs`) | optional(`Needs`) | optional |

第二次 call optional 的話就 no-op;第二次 call required 會把之前的 optional 升級成 required。所以 `Needs` 到處用沒關係 — 如果其他地方已經 required 了,`Needs` 會安靜地退讓。

## Runtime 用:`ms.Resolve[T]`

在 `ms.Needs` 包的 handler 裡面,用之前先檢查 dep 有沒有真的在:

```go
func onVideoCompleted(w http.ResponseWriter, r *http.Request) {
    if video, ok := ms.Resolve[videoclient.Client]("video"); ok {
        // video module 有裝 — 用它
        video.MarkProcessed(r.Context(), videoID)
    }
    // 沒裝就 skip
}
```

**v1 note**:Cross-module client wiring 還沒做。`Resolve[T]` 現在永遠 return `(zero, false)`。API shape 已經 commit 了,所以今天寫的 code 之後 real resolution 上線時會自動 work。

## Versioning

`DependsOn` 目前沒有 version constraint。Breaking change 的處理方式是 bump module ID(例如 `oauth-core` → `oauth-core-v2`),跟 Go stdlib 的 `database/sql` vs `database/sql/v2` 同一套 pattern。

Semver range 的 support(`>=1.0.0 <2.0.0`)等 catalog 大到需要 constraint solver 再說。
