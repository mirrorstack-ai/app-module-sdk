# Scopes

> Language: [English](../../concepts/scopes.md) · **繁體中文**

每個 HTTP route 屬於三個 **scope** 的其中之一。Scope 決定 SDK 套用哪一層 auth middleware,也決定哪些呼叫者可以打到這個 route。

| Scope | 註冊函式 | 認證方式 | 呼叫者 |
|---|---|---|---|
| **Platform** | `ms.Platform(fn)` | Session + role(`auth.PlatformAuth`) | 已登入的 dashboard 使用者(host frontend) |
| **Public** | `ms.Public(fn)` | 無 | 匿名(webhooks、OAuth callbacks、公開 API) |
| **Internal** | `ms.Internal(fn)` | HMAC(`auth.InternalAuth`) | 平台本身(lifecycle、events、crons) |

## 讀取自己的 app id

在 Platform 與 Public route **兩者**,都從受信任的 context identity 讀取自己模組的 app id,**不要**從 request 資料(query string、body、path)讀 — 那些是呼叫者可控、可偽造的:

```go
appID := ms.AppID(r.Context())
```

SDK 會在你的 handler 執行**之前**,把平台經 dispatch 注入、受信任的 app id 提升進 context:Platform 透過 session auth,Public 透過 proxy guard 驗證過 token 的路徑(guard 證明 request 確實經過 dispatch,所以它轉發的 `X-MS-App-ID` 是可信的)。`ms.AppID` 會回傳它;只有在沒有設定 platform token 的獨立單元測試裡才回傳 `""`。`ms.AppID` 是 `ms.WithAppID` 的「入站對偶」(`ms.WithAppID` 用來把一個 *對外* 的 `ms.Call` *改指向* 另一個 app)。

## Platform

已登入的 dashboard 使用者。SDK 會檢查 platform auth flow 發出的 session token。Route 可以從 context 取得 `auth.Identity`,內含 `AppID`、`UserID`、`AppRole` — 用 `ms.AppID(r.Context())` 讀取 app id(見上)。

```go
ms.Platform(func(r chi.Router) {
    r.Get("/items", listItems)
    r.With(ms.RequirePermission("items.write", p.Admin(), p.Viewer())).Post("/items", createItem)
})
```

需要以角色控管存取時,加上 `ms.RequirePermission(name, roles...)`。這個函式會同時掛上 Chi middleware,並把 permission 註冊到 manifest 裡,platform 的安裝畫面才看得到。

## Public

匿名 — 沒有認證。用在:

- OAuth callback route(`/oauth/google/callback`)
- 第三方 webhook(`/webhooks/stripe`)
- 任何人都能打的公開 API

```go
ms.Public(func(r chi.Router) {
    r.Get("/oauth/google/callback", handleGoogleCallback)
})
```

SDK 在這層不做使用者認證,但 proxy guard 仍然守在每個 Public route 前面:沒有經過平台 proxy 的 request 會被以 `403 not_proxied` 拒絕。因為 guard 驗證過 proxy token,它提升出來的 app id(`ms.AppID(r.Context())`)是可信的 — 用它,不要從 request 讀 app id。但如果 payload 裡宣稱了 *使用者* 身分(signed webhook、OAuth state nonce 等),你還是必須自己驗證。

## Internal

Platform 打 module。Request 必須帶 `X-MS-Internal-Secret: <shared secret>`(透過 `MS_INTERNAL_SECRET` 環境變數設定);只要有設定 secret 來源,SDK 就會把沒帶或帶錯的 request 擋掉,回 401。純本機執行、沒有設定 secret 時,SDK 會直接放行,讓 `mirrorstack dev` 可以自己 curl 自己的 route ——`--tunnel` 與部署後的模式都會設定 secret。

**這個 secret 不是呼叫端的邊界。** 它擋得住「直接」打到你的 module(它的 localhost port、它的 function target)而且沒帶 secret 的呼叫端,但擋不住瀏覽器。平台的 `/module/{moduleID}/*` edge 在 HTTP 層是完全沒有驗證的,只會拒絕 `__mirrorstack/*` 這個 namespace,並且會把 `/internal/...` 轉發給你的 module,同時由平台自己補上 internal secret 和寫死的 `admin` app-role。因此只要能打到那個 edge 的人,就能打到 `Internal` route ——參見 [audit trails](../../concepts/audit.md) §7。

用在:

- Lifecycle:install / upgrade / downgrade / uninstall
- 事件派送:`POST /__mirrorstack/events/<name>`
- Cron 觸發:`POST /__mirrorstack/crons/<name>`
- Task worker 分派:`POST /__mirrorstack/tasks/<name>`
- Manifest:`GET /__mirrorstack/platform/manifest`
- MCP surface:`GET/POST /__mirrorstack/mcp/*`

Internal route 不論 mode 為何,都套用 1 MB 的 request body 上限(`MaxBytesReader`)。

```go
ms.Internal(func(r chi.Router) {
    r.Post("/rebuild-index", rebuildIndex)  // 平台觸發的維護任務
})
```

## Auth 對照表

| Request 帶… | Platform | Public | Internal |
|---|---|---|---|
| 什麼都沒帶 | 401 | 200 | 401 |
| 過期或無效的 session | 401 | 200 | 401 |
| 有效 session,但角色不符 | 403 | 200 | 401 |
| 有效 session,角色符合 | 200 | 200 | 401 |
| 只帶 internal secret | 401 | 200 | 200 |

這張表只描述有設定 secret 來源時,SDK middleware 獨立運作的結果,不是 reachability 對照表。沒有設定 secret 來源時(純本機執行、獨立單元測試),Platform 會產生一個合成的本機 admin,Internal 則完全略過檢查;而且從平台的 `/module` edge 進來的 request 不論是誰送的,都已經帶好 credential。Public 欄只顯示 user-auth 檢查 — Public route 前面另外還有 proxy guard,未經平台送進來的 request 會收到 `403 not_proxied`,這與每一列描述的 session credential 彼此獨立。

SDK 的 middleware 依 scope 各自運作,不接受其他 scope 的 credential:session token 無法通過 `InternalAuth`,internal secret 也無法通過 `PlatformAuth`。這是在描述 middleware,不是 reachability — 上面的 platform edge 會提供 scope 所要求的 credential。Scope 是你組織 surface 的方式;它不是機密性邊界。

## 如何選 scope

- **Dashboard 使用者觸發的動作** → Platform
- **平台自己驅動的** → Internal
- **匿名外部呼叫者需要打的** → Public

不確定時一律放 Internal:它會讓 route 不出現在匿名 public surface,也不會進到 dashboard 以 role gate 控管的 surface,而且它的意圖("由平台驅動")日後最容易讀懂。不要把這誤認為 security decision — `Internal` route 上沒有任何東西會對能打到 `/module` edge 的人隱藏。如果公開這些資料會造成傷害,保護就必須放在 handler 裡,或放在你儲存的內容裡;不能放在 scope 上。
