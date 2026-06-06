# Internal calls — envelope, typed client, CORS (DESIGN — proposed)

> **Status: proposed.** Design for review; not yet implemented. Decision
> context in [ADR 0005](../../../mirrorstack-docs/adr/0005-module-internal-scope-stays-rest.md):
> the Internal scope stays REST; this doc adds the three ergonomics that bank
> billing-engine's useful parts without its routing shape.

Three additive pieces. Parts 1 and 3 ship independently today. Part 2 (the
typed client) is **gated** on the prod dispatch→module sender (see §4).

---

## 1. Response envelope helper (`internal/httputil`)

Today handlers write `httputil.JSON(w, status, v)` and the only standard
error shape is `ErrorResponse{Error string}` → `{"error":"…"}`. Cross-module
callers have no uniform success/failure discriminator. Add billing's
envelope (`billing-engine/cmd/account-api/main.go`) as opt-in helpers:

```go
// Envelope is the uniform Internal response shape (mirrors billing-engine).
type Envelope struct {
	OK       bool      `json:"ok"`
	Response any       `json:"response,omitempty"`
	Error    *EnvError `json:"error,omitempty"`
}

type EnvError struct {
	Code    string `json:"code"`              // machine code, e.g. "not_found"
	Message string `json:"message,omitempty"` // human detail, optional
}

// OK writes {ok:true, response:v}.
func OK(w http.ResponseWriter, status int, v any)

// Fail writes {ok:false, error:{code,message}}.
func Fail(w http.ResponseWriter, status int, code, message string)
```

- **Scope of adoption:** Internal handlers should use `OK`/`Fail` — that is
  the machine-to-machine boundary the typed client (§2) consumes. Platform/
  Public responses are consumed by the browser/bundle and keep their existing
  shape; no forced migration.
- **Back-compat:** `JSON` and `ErrorResponse` stay. This is purely additive.
- **Decision point:** do we migrate the example modules' existing `writeError`
  (`{"error":…}`) to `Fail` for consistency, or leave them? *Recommendation:
  migrate oauth-core's Internal handlers only, leave Platform/Public as-is.*

---

## 2. Typed cross-module client (`ms.CallModule`)

One generic client replaces N hand-written per-procedure wrappers (billing's
client is hand-written 6-line wrappers — exactly what the SDK should remove).
Go forbids type params on methods, so this is a package-level generic func:

```go
// CallModule invokes a declared dependency's Internal route and decodes the
// {ok,error} envelope into Resp. dep is the dependency slug declared via
// ms.DependsOn. The inbound request's trusted identity (user/app/role) is
// propagated so the call runs on-behalf-of the same caller.
func CallModule[Req, Resp any](
	ctx context.Context, dep, method, path string, req Req,
) (Resp, error)
```

Behavior:
1. **Resolve** the dep's Internal base from platform-injected dep routing
   (see §4 — this is the gated piece).
2. **Auth + identity:** attach the platform/internal secret, and forward
   `X-MS-User-ID`/`X-MS-App-ID`/`X-MS-App-Role` read from `ctx` (the same
   trusted fields `NewLambdaHandler` injected on the inbound request). This
   preserves the trust model — the dep sees the original caller's identity,
   not the calling module's.
3. **Send** `method` + `path` + `json(req)`. Prod rides `lambda.Invoke` via
   the platform sender (HTTP-shaped `LambdaRequest`); dev rides HTTP through
   the dispatch proxy. The module author writes the same call either way.
4. **Decode** the `Envelope`. `ok:true` → unmarshal `response` into `Resp`.
   `ok:false` → return a typed `*CallError{Code, Message, Status}`.

```go
type CallError struct {
	Code    string
	Message string
	Status  int
}
func (e *CallError) Error() string
```

Usage (a consumer reading an oauth-core user):
```go
user, err := ms.CallModule[struct{}, UserDTO](
	r.Context(), "oauth-core", "GET", "/internal/users/"+id, struct{}{})
```

- **Type safety parity with RPC, without codegen or per-method wrappers.**
- **Decision point:** package the request as a body for GET? Internal `GET`
  with a body is fine over `lambda.Invoke` (it's a field on `LambdaRequest`),
  but awkward over real HTTP. *Recommendation: for GETs, require `Req = struct{}{}`
  and put params in `path`; bodies only on POST/PUT/DELETE.*

---

## 3. Drop CORS on the Internal mount

Today `localDevCORS` is applied **router-wide** in the dev-bypass branch
(`module.go:151-163`, `m.router.Use(localDevCORS)` when `MS_INTERNAL_SECRET`
is unset). Internal routes are proxy/lambda-only — a browser never originates
an Internal call — so CORS there is dead weight and the one bit of genuine
"web baggage" the RPC camp correctly disliked.

**Change:** apply `localDevCORS` only to the Platform and Public scoped
subrouters, never to Internal (and never to the system internal routes under
`/__mirrorstack/*`). Sketch: move the `Use(localDevCORS)` out of the global
router and into `scopedRoutes` for `ScopePlatform`/`ScopePublic` only, or
guard it by scope prefix. No behavior change for browser-facing scopes;
Internal simply stops emitting CORS headers.

---

## 4. Gating dependency: the prod dispatch→module sender

§2's `CallModule` needs to **resolve a dep's Internal base** and needs the
platform to actually carry the call in prod. Neither exists yet — today's
built path is the WSS dev tunnel (`api-platform/internal/dispatch/handler/dev_tunnel.go`);
there is no prod module-invoke sender. Per ADR 0005 that sender MUST forward
`{method, path, body}` into the SDK's HTTP-shaped `LambdaRequest`.

So sequencing:
- **Now (independent):** §1 envelope helper, §3 CORS scoping.
- **Gated on the sender (task #116):** §2 typed client + dep-routing
  injection (platform supplies each declared dep's reachable base, e.g. via
  `Resources` or a `MS_DEP_<slug>_URL`-style map the SDK reads).

Open question to settle when the sender lands: **how is dep routing injected** —
as part of `runtime.Resources`, or a separate deps map? That choice belongs
with the sender's design, not here.
