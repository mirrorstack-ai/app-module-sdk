# Dependencies

> Language: [English](../../concepts/dependencies.md) · **繁體中文**

MirrorStack module 用 **module ID** 宣告對其他 module 的相依關係,而不是抽象的 capability name。平台的 catalog 讀這些宣告來決定安裝順序(required deps),也讓 module 之間能選擇性整合(optional deps)。

## Required vs optional

| | Required | Optional |
|---|---|---|
| **宣告方式** | 在 root 用 `ms.DependsOn(spec)` | 用 `ms.Needs(spec, handler)` 包住 handler |
| **安裝時行為** | Catalog 會先裝這個相依 module;找不到、或沒有版本符合 constraint,就安裝失敗。 | Catalog 不檢查;你的 module 可以單獨安裝。 |
| **解除安裝行為** | 你的 module 還在的話,相依 module 不能被解除安裝。 | 相依 module 可以隨時解除安裝;你的 module 照常運作。 |
| **Runtime 保證** | 一定存在。 | 要先用 `ms.Resolve[T](id)` 檢查。 |
| **Manifest 輸出** | `{"id":"oauth-core","version":"^1.2.0"}` | `{"id":"video","version":"^1","optional":true}` |

## Version constraints

`DependsOn` 跟 `Needs` 都接受 `"id"`(任何版本)或 `"id@constraint"` 這種 spec。Constraint 使用 npm 風格的 SemVer 語法,在註冊時就會驗證 — 寫錯會立刻 panic。

| Spec | 接受的版本 |
|---|---|
| `"oauth-core"` | 任何版本 |
| `"oauth-core@^1.2.0"` | `>=1.2.0, <2.0.0` — 相容於相同 major |
| `"oauth-core@~1.2.0"` | `>=1.2.0, <1.3.0` — 相容於相同 minor |
| `"oauth-core@1.x"` | 任何 `1.x.x` |
| `"oauth-core@>=1.2.0 <2.0.0"` | 明確範圍 |
| `"oauth-core@1.2.3"` | 精確版本 |

Catalog 在安裝時會檢查 constraint。沒寫 = 作者接受任何版本;有寫 = catalog 會拒絕不相容的版本。

## Required:`ms.DependsOn`

在 module init 時宣告一次。沒裝這個相依 module,你的 module 根本起不來。

```go
func main() {
    ms.Init(...)
    ms.DependsOn("oauth-core@^1.2.0")   // required,1.2.0 以後、2.0.0 以前的版本都接受
    ms.Start()
}
```

`ms.DependsOn` 一律註冊為 required — 沒有依位置判斷的魔法。放在 `main()` 或 package-level `init()` 都行,語意一樣。

## Optional:`ms.Needs` 包住 handler

Optional 相依在**註冊 handler 的同一行**一起宣告 — 跟實際使用它的程式碼放在一起。`ms.Needs(spec, handler)` 會把相依註冊為 optional,並原封不動地回傳 handler,所以可以跟任何吃 `http.HandlerFunc` 的 API 搭配。

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

1. 相依宣告跟 handler 寫在同一行,不用多繞「這個 setup function 是做什麼用的」一圈。
2. 不靠呼叫位置做自動判斷。`ms.DependsOn` 語意明確,只有一種解讀。Extract-function 重構不會偷偷改變分類。
3. 同一組 API 在 `OnEvent`、`Cron`、chi route、任何吃 handler 的地方都通用。

### 一個 handler 要多個 optional 相依

把 `Needs` 疊起來:

```go
ms.OnEvent("payment", ms.Needs("billing", ms.Needs("audit-log", onPayment)))
```

每一層 `Needs` 註冊一個相依;最外層那次呼叫會回傳包好的 handler。

### Chi route 範例

```go
ms.Platform(func(r chi.Router) {
    r.Get("/transcode", ms.Needs("video", transcodeHandler))
    r.Post("/ship",     ms.Needs("billing", ms.Needs("shipping", shipHandler)))
})
```

## Dedup 規則

同一個相依在程式碼裡宣告多次的話,**required 勝出**:

| 第一次宣告 | 第二次宣告 | 最後存起來的 |
|---|---|---|
| required(`DependsOn`) | required | required |
| required(`DependsOn`) | optional(`Needs`) | required |
| optional(`Needs`) | required(`DependsOn`) | required(升級) |
| optional(`Needs`) | optional(`Needs`) | optional |

第二次宣告成 optional 會被當成 no-op;第二次宣告成 required 會把之前的 optional 升級成 required。所以 `Needs` 可以放心地到處使用 — 如果別的地方已經宣告成 required 了,`Needs` 會安靜地退讓。

## Runtime 使用:`ms.Resolve[T]`

在 `ms.Needs` 包起來的 handler 裡,使用相依之前先檢查它存不存在:

```go
func onVideoCompleted(w http.ResponseWriter, r *http.Request) {
    if video, ok := ms.Resolve[videoclient.Client]("video"); ok {
        // video module 有裝 — 直接使用
        video.MarkProcessed(r.Context(), videoID)
    }
    // 沒裝就略過
}
```

**v1 說明**:跨 module 的 client wiring 還沒實作,`Resolve[T]` 目前一律回傳 `(zero, false)`。API 形狀已經固定,今天寫的程式碼在真正的 resolution 機制上線後會自動運作,不用改。
