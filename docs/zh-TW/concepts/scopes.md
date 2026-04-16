# Scopes

> Language: [English](../../concepts/scopes.md) · **繁體中文**

每個 HTTP route 都屬於三個 **scope** 的其中之一。Scope 決定 SDK 要套哪個 auth middleware,還有哪些 client 可以 call 得到這個 route。

| Scope | 進入點 | Auth | Call 方 |
|---|---|---|---|
| **Platform** | `ms.Platform(fn)` | Session + role(`auth.PlatformAuth`) | Dashboard users(host frontend) |
| **Public** | `ms.Public(fn)` | 無 | 匿名(webhooks、OAuth callbacks、public APIs) |
| **Internal** | `ms.Internal(fn)` | HMAC(`auth.InternalAuth`) | Platform 自己(lifecycle、events、crons) |

## Platform

有登入的 dashboard 使用者。SDK 會檢查 session token(platform 的 auth flow 會 set)。Route 可以從 context 拿到 `auth.Identity`,裡面有 `AppID`、`UserID`、`AppRole`。

```go
ms.Platform(func(r chi.Router) {
    r.Get("/items", listItems)
    r.With(ms.RequirePermission("items.write", p.Admin(), p.Viewer())).Post("/items", createItem)
})
```

要做 role-based gating 就加 `ms.RequirePermission(name, roles...)`。它會同時裝 Chi middleware 跟把 permission declare 到 manifest,platform 的 install 畫面才能顯示出來。

## Public

匿名 — 沒有 auth。用在:

- OAuth callback routes(`/oauth/google/callback`)
- 第三方 webhooks(`/webhooks/stripe`)
- 誰都可以打的 public API

```go
ms.Public(func(r chi.Router) {
    r.Get("/oauth/google/callback", handleGoogleCallback)
})
```

SDK 在這邊不做任何 auth,但是對於 payload 裡 claim 身份的內容(signed webhook、OAuth state nonce 這些),你要自己負責 verify。

## Internal

Platform 才能 call。request 要帶 `X-MS-Internal-Secret: <shared secret>`(靠 `MS_INTERNAL_SECRET` env var 設定)。SDK 會把其他的都擋掉,回 401。

用在:

- Lifecycle:install / upgrade / downgrade / uninstall
- Event delivery:`POST /__mirrorstack/events/<name>`
- Cron fire:`POST /__mirrorstack/crons/<name>`
- Task worker dispatch:`POST /__mirrorstack/tasks/<name>`
- Manifest:`GET /__mirrorstack/platform/manifest`
- MCP surface:`GET/POST /__mirrorstack/mcp/*`

Internal route 不管在哪個 mode 都有 1 MB request body cap(`MaxBytesReader`)。

```go
ms.Internal(func(r chi.Router) {
    r.Post("/rebuild-index", rebuildIndex)  // platform 觸發的維護工作
})
```

## Auth 對照表

| Request 帶… | Platform | Public | Internal |
|---|---|---|---|
| 什麼都沒帶 | 401 | 200 | 401 |
| 過期/無效的 session | 401 | 200 | 401 |
| 有效 session,角色錯 | 403 | 200 | 401 |
| 有效 session,角色對 | 200 | 200 | 401 |
| 只帶 internal secret | 401 | 200 | 200 |

不同 scope 的 route **互不相通** — 一個 scope 的 caller 再怎麼認證都打不到另一個 scope 的 route,是獨立的 auth 領域。

## 怎麼選 scope

- **Dashboard user 觸發的動作** → Platform
- **Platform 自己驅動的** → Internal
- **匿名外部 caller 需要打的** → Public

不確定的話,default 給 Internal。以後要 expose 出來很簡單,但已經 leak 出去的就裝不回去了。
