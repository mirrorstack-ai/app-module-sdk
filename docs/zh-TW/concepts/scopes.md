# Scopes

> Language: [English](../../concepts/scopes.md) · **繁體中文**

每個 HTTP route 屬於三個 **scope** 的其中之一。Scope 決定 SDK 套用哪一層 auth middleware,也決定哪些呼叫者可以打到這個 route。

| Scope | 註冊函式 | 認證方式 | 呼叫者 |
|---|---|---|---|
| **Platform** | `ms.Platform(fn)` | Session + role(`auth.PlatformAuth`) | 已登入的 dashboard 使用者(host frontend) |
| **Public** | `ms.Public(fn)` | 無 | 匿名(webhooks、OAuth callbacks、公開 API) |
| **Internal** | `ms.Internal(fn)` | HMAC(`auth.InternalAuth`) | 平台本身(lifecycle、events、crons) |

## Platform

已登入的 dashboard 使用者。SDK 會檢查 platform auth flow 發出的 session token。Route 可以從 context 取得 `auth.Identity`,內含 `AppID`、`UserID`、`AppRole`。

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

SDK 在這層不做任何認證,但如果 payload 裡宣稱了身分(signed webhook、OAuth state nonce 等),你必須自己驗證。

## Internal

只有平台能打。Request 必須帶 `X-MS-Internal-Secret: <shared secret>`(透過 `MS_INTERNAL_SECRET` 環境變數設定)。SDK 會拒絕其他所有 request,回 401。

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

不同 scope 的 route **互不相通** — 一邊的呼叫者無論怎麼認證,都打不到另一邊的 route,是獨立的認證網域。

## 如何選 scope

- **Dashboard 使用者觸發的動作** → Platform
- **平台自己驅動的** → Internal
- **匿名外部呼叫者需要打的** → Public

不確定時一律放 Internal。以後要對外公開很簡單,但外洩出去之後要收回來很困難。
