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

**The secret is what the SDK middleware checks.** Forwarder identity headers are promoted only on the secret-validated path, never on the local bypass or a rejection; empty headers never mint an identity. `TestCharacterization_ForwarderIdentityTrustedOnlyOnSecretValidatedPath` pins that behavior. Who can reach a given route class through the platform is a property of the platform edge, pinned by api-platform's `TestModuleEdge_RouteClassTrustMatrix`, not by this page.

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

Measured on the real `m.Router()` with `MS_INTERNAL_SECRET` set and the process not in Lambda. "Identity headers" means all three of `X-MS-User-ID` / `X-MS-App-ID` / `X-MS-App-Role`:

| Request carries | Platform | Public | Internal |
|---|---:|---:|---:|
| Nothing | 403 | 403 | 401 |
| Internal secret only | 401 | 200 | 200 |
| Internal secret + identity headers | 200 | 200 | 200 |
| Wrong secret + identity headers | 403 | 403 | 401 |
| Identity headers only, no secret | 403 | 403 | 401 |

The 403 responses are the proxy guard's `not_proxied`. The Platform 401 with only the internal secret is caused by missing identity headers, not by the secret. With no secret source configured, every cell is 200.

One `X-MS-Internal-Secret` authenticates both the Internal and Platform scopes: `PlatformAuth` and `InternalAuth` read the same secret source, so they are not disjoint credential domains. `TestCharacterization_InternalSecretSatisfiesPlatformAuth_ScopesAreNotDisjoint` pins this behavior. Scope is how you organize your surface; it is not a confidentiality boundary.

[`auth_scope_truth_test.go`](../../auth_scope_truth_test.go) is the executable version of this section; a change here must change that file too.

## Picking a scope

- **User-triggered action from the dashboard** → Platform
- **Something the platform itself drives** → Internal
- **Something anonymous external callers need** → Public

If you're unsure, default to Internal: it keeps the route off the anonymous public surface and out of the dashboard's role-gated one, and its intent ("the platform drives this") is the easiest to read later. Do not mistake that for a security decision. If exposing the data would be harmful, the protection has to live in the handler, or in what you store; not in the scope.
