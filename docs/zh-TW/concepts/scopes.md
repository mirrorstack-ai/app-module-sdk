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

SDK 會在 handler 執行**之前**,把平台的 authoritative app id 提升進
context。現在 direct HTTP、WSS/local 與 Lambda 都會在平台認證後消耗同一份
[typed invocation context](./invocation.md);raw wire 與 compatibility headers
不會進入 handler。`ms.AppID` 會回傳提升後的值;只有在沒有安裝 identity 的
情況(例如獨立單元測試)才回傳 `""`。`ms.AppID` 是 `ms.WithAppID` 的「入站
對偶」(`ms.WithAppID` 用來把一個 *對外* 的 `ms.Call` *改指向* 另一個 app)。

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

**secret 是 SDK middleware 檢查的 credential。** Typed invocation 只會在
credential 驗證成功後解析,而且 method、path、body、app/module scope 以及任何
dual-send 的 legacy claims 都必須一致。Local bypass 會移除 caller 提供的 typed
header,不會信任它。在 bounded compatibility window 內,forwarder identity
headers 也只會在 secret 驗證通過的路徑被提升;空 header 不會建立 identity。
誰能透過平台打到某個 route class,仍是 platform edge 的屬性,由 api-platform
的 `TestModuleEdge_RouteClassTrustMatrix` 固定。

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

下表描述 bounded legacy projection 在真正 `m.Router()` 上的行為:已設定
`MS_INTERNAL_SECRET`,且 process 不在 Lambda 中。新的平台 traffic 使用上面
的 typed invocation。「identity headers」指 `X-MS-User-ID` / `X-MS-App-ID` /
`X-MS-App-Role` 三個全帶:

| Request 帶… | Platform | Public | Internal |
|---|---:|---:|---:|
| 什麼都沒帶 | 403 | 403 | 401 |
| 只帶 internal secret | 401 | 200 | 200 |
| internal secret + identity headers | 200 | 200 | 200 |
| 錯誤的 secret + identity headers | 403 | 403 | 401 |
| 只帶 identity headers,沒有 secret | 403 | 403 | 401 |

表中的 403 都是 proxy guard 的 `not_proxied`。只帶 internal secret 時,Platform 的 401 是因為缺少 identity headers,不是 secret。如果沒有設定 secret 來源,每一格都是 200。

同一個 `X-MS-Internal-Secret` 可以驗證 Internal 與 Platform 兩個 scope:`PlatformAuth` 與 `InternalAuth` 讀取相同的 secret 來源,因此兩者不是互不相交的 credential domain。`TestCharacterization_InternalSecretSatisfiesPlatformAuth_ScopesAreNotDisjoint` 固定了這項行為。Scope 是你組織 surface 的方式;它不是機密性邊界。

[`auth_scope_truth_test.go`](../../../auth_scope_truth_test.go) 是本節的 executable version;修改本節時,也必須修改該檔案。

## 如何選 scope

- **Dashboard 使用者觸發的動作** → Platform
- **平台自己驅動的** → Internal
- **匿名外部呼叫者需要打的** → Public

不確定時一律放 Internal:它會讓 route 不出現在匿名 public surface,也不會進到 dashboard 以 role gate 控管的 surface,而且它的意圖("由平台驅動")日後最容易讀懂。不要把這誤認為 security decision。如果公開這些資料會造成傷害,保護就必須放在 handler 裡,或放在你儲存的內容裡;不能放在 scope 上。
