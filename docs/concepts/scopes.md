# Scopes

> Language: **English** · [繁體中文](../zh-TW/concepts/scopes.md)

Every HTTP route lives under one of three **scopes**. The scope determines which auth middleware the SDK applies and which clients can reach the route.

| Scope | Entry point | Auth | Intended caller |
|---|---|---|---|
| **Platform** | `ms.Platform(fn)` | Session + role (`auth.PlatformAuth`) | Dashboard users (host frontend) |
| **Public** | `ms.Public(fn)` | None | Anonymous (webhooks, OAuth callbacks, public APIs) |
| **Internal** | `ms.Internal(fn)` | HMAC (`auth.InternalAuth`) | Platform itself (lifecycle, events, crons) |

## Reading your app id

On **both** Platform and Public routes, read your module's app id from the trusted context identity — never from request data (query string, body, path), which the caller controls and can forge:

```go
appID := ms.AppID(r.Context())
```

The SDK promotes the platform's trusted, dispatch-injected app id into the context **before your handler runs**: on Platform via the session auth, on Public via the proxy guard's validated-token path (the guard proves the request came through dispatch, so the `X-MS-App-ID` header it forwards is trustworthy). `ms.AppID` returns it; it returns `""` only in a standalone unit test where no platform token is configured. `ms.AppID` is the inbound twin of `ms.WithAppID` (which *retargets* an outbound `ms.Call` at a different app).

## Platform

Authenticated users of the host dashboard. The SDK checks a session token set by the platform's auth flow. Routes receive an `auth.Identity` via context with `AppID`, `UserID`, and `AppRole` — read the app id with `ms.AppID(r.Context())` (see above).

```go
import p "github.com/mirrorstack-ai/app-module-sdk/roles"

ms.Platform(func(r chi.Router) {
    r.Get("/items", listItems)
    r.With(ms.RequirePermission("items.write", p.Admin())).Post("/items", createItem)
})
```

Add `ms.RequirePermission(name, roles...)` for role-based gating. Roles come from the `roles` package (`p.Admin()`, `p.Viewer()`, `p.Custom("key")`). It both installs the Chi middleware and declares the permission on the manifest so the platform's install screen can display it.

## Public

Anonymous — no auth. Use for:

- OAuth callback routes (`/oauth/google/callback`)
- Third-party webhooks (`/webhooks/stripe`)
- Public APIs that anyone can hit

```go
ms.Public(func(r chi.Router) {
    r.Get("/oauth/google/callback", handleGoogleCallback)
})
```

The SDK does not run user auth here, but the proxy guard still fronts every Public route: a request that did not come through the platform proxy is rejected with `403 not_proxied`. Because the guard validated the proxy token, the app id it promotes (`ms.AppID(r.Context())`) is trusted — use it instead of reading an app id off the request. You are still responsible for verifying payloads that claim *user* identity (signed webhooks, OAuth state nonces, etc.).

## Internal

Platform-to-module. Requests must carry `X-MS-Internal-Secret: <shared secret>` (via `MS_INTERNAL_SECRET` env var); once a secret source is configured the SDK rejects a missing or wrong secret with 401. On a bare local run with no secret configured the SDK bypasses the check so `mirrorstack dev` can curl its own routes — `--tunnel` and deployed mode both configure one.

**The secret is not a caller boundary.** It stops a caller that reaches your module *directly* — its localhost port, its function target — without the secret. It does not stop a browser. The platform's `/module/{moduleID}/*` edge is unauthenticated at the HTTP layer, refuses only the `__mirrorstack/*` namespace, and forwards `/internal/...` to your module with the internal secret and a hardcoded `admin` app-role that the platform supplies itself. An `Internal` route is therefore reachable by anyone who can reach that edge — see [audit trails](audit.md) §7.

Used for:

- Lifecycle: install / upgrade / downgrade / uninstall
- Event delivery: `POST /__mirrorstack/events/<name>`
- Cron fires: `POST /__mirrorstack/crons/<name>`
- Task worker dispatch: `POST /__mirrorstack/tasks/<name>`
- Manifest: `GET /__mirrorstack/platform/manifest`
- MCP surface: `GET/POST /__mirrorstack/mcp/*`

Internal routes have a 1 MB request body cap (`MaxBytesReader`) regardless of mode.

```go
ms.Internal(func(r chi.Router) {
    r.Post("/rebuild-index", rebuildIndex)  // platform-triggered maintenance
})
```

## The auth matrix

| Request has… | Platform | Public | Internal |
|---|---|---|---|
| Nothing | 401 | 200 | 401 |
| Expired / invalid session | 401 | 200 | 401 |
| Valid session, wrong role | 403 | 200 | 401 |
| Valid session, right role | 200 | 200 | 401 |
| Internal secret only | 401 | 200 | 200 |

This is the SDK middleware in isolation, with a secret source configured. It is not a reachability table. With no secret source configured (a bare local run, a standalone unit test) Platform mints a synthetic local admin and Internal bypasses its check entirely; and a request arriving through the platform's `/module` edge comes pre-credentialed no matter who sent it. The Public column shows the user-auth check only — Public routes are additionally fronted by the proxy guard, which answers `403 not_proxied` to a request that did not come through the platform, orthogonally to the session credential each row describes.

The SDK's middlewares are per-scope and do not accept each other's credentials: a session token does not satisfy `InternalAuth`, and the internal secret does not satisfy `PlatformAuth`. That is a statement about the middleware, not about reachability — the platform edge above supplies whichever credential the scope asks for. Scope is how you organize your surface; it is not a confidentiality boundary.

## Picking a scope

- **User-triggered action from the dashboard** → Platform
- **Something the platform itself drives** → Internal
- **Something anonymous external callers need** → Public

If you're unsure, default to Internal: it keeps the route off the anonymous public surface and out of the dashboard's role-gated one, and its intent ("the platform drives this") is the easiest to read later. Do not mistake that for a security decision — nothing on an `Internal` route is hidden from whoever can reach the `/module` edge. If exposing the data would be harmful, the protection has to live in the handler, or in what you store; not in the scope.
