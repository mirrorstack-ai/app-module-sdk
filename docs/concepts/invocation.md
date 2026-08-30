# Trusted invocation context

> Language: **English** · [繁體中文](../zh-TW/concepts/invocation.md)

Every request that API Platform serves to a module carries one versioned,
transport-neutral invocation context. The SDK authenticates the platform,
validates the context against the actual method, path, body and module, removes
its wire representation, and only then calls your handler.

Read it from the request context:

```go
import "github.com/mirrorstack-ai/app-module-sdk/invocation"

func handle(w http.ResponseWriter, r *http.Request) {
    trusted, ok := invocation.FromContext(r.Context())
    if !ok {
        // A standalone test or a legacy caller did not supply typed context.
        http.Error(w, "trusted invocation required", http.StatusUnauthorized)
        return
    }

    requestID := trusted.Request.ID
    userID := trusted.Identity.UserID
    publicBase := trusted.Routes.Public
    _ = requestID
    _ = userID
    _ = publicBase
}
```

`FromContext` is deliberately the only context operation exposed to module
code. There is no public setter or wire decoder: a module must not turn a raw
header into something that looks platform-authored. The returned value owns
copies of its slices, so changing `Redirects` or `Capabilities` cannot mutate
the request context.

## What the fields mean

| Field | Meaning |
|---|---|
| `App` / `Module` | Authoritative installation and callee identity. |
| `Identity` | `member`, `platform`, or actorless; platform role and delegation are explicit. |
| `Routes` | Canonical origin and module/public/platform paths plus an array of allowed redirects. |
| `Request` | Occurrence ID, method, local path, body digest and occurrence time. |
| `Trust` | Edge-derived source, client IP when known, host, scheme and origin. |
| `Cookies` | Platform-owned cookie namespace and migration capabilities. |
| `Audit.Provenance` | Opaque, occurrence-bound platform evidence for audit ingestion. |

For routine authorization, keep using `ms.UserID`, `ms.AppID`, `ms.AppRole`
and `ms.RequirePermission`. Use the invocation context when you need the wider
request contract: identity namespace, canonical routes, redirects, trusted
connection facts, request occurrence, cookie capability or audit provenance.

## Trust rules

- Never read `X-MS-Invocation` or the legacy `X-MS-*` identity headers. The SDK
  consumes them and handlers must not depend on a transport representation.
- Never construct a physical cookie name. Continue setting and reading logical
  cookie names; API Platform maps them into the owning app/module namespace.
- Treat `Audit.Provenance` as opaque. Preserve it unchanged for the platform
  audit ingress; do not decode, verify, alter or log it.
- `Trust.ClientIP` may be empty. Do not invent a value or fall back to an
  untrusted forwarding header.
- `FromContext` may be absent in isolated unit tests and during the bounded
  legacy migration. Tests that require production trust should reject absence
  or exercise the SDK transport adapter, rather than manufacturing a context.

Direct HTTP, WSS/local relay and deployed Lambda consume the same canonical v1
bytes. During migration the platform may dual-send legacy claims through
2026-11-30; if a legacy value conflicts with the typed context, the SDK rejects
the request before the handler runs.
