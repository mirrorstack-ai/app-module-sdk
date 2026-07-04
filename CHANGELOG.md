# Changelog

All notable changes to the MirrorStack Module SDK.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

A module can now write into its app's notification feed. The design mirrors `ms.Emit` exactly — same context-derived app scope, same dispatch-HTTP transport, same #146 prod seam — so notifications inherit the trust model already proven for events: the module supplies content, the dispatch re-derives the sender identity from the live session and never trusts the envelope.

### Added
- **`ms.Notify(ctx, ms.Notification{...})`** — sends an in-app notification to the current app's members. `Notification` carries an i18n `Title` (required) and `Body` (optional) as `ms.Text`/`ms.T` Labels resolved to per-locale maps **at send time** (the platform picks each recipient's locale, the module never does), plus optional `Icon`/`Link` and an `Audience` (`ms.NotifyAdmins`, the default, or `ms.NotifyAllMembers`; anything else is an error). The SDK POSTs a `{id, sentAt, sourceModuleID, title, body, icon, link, audience}` envelope to the platform dispatch notification ingress at `{MS_DISPATCH_URL | dev fallback}/apps/{appID}/notifications` — the same transport idiom as `ms.Emit`/`ms.Call`, with the same error contract: an empty app-scope context, a Title that resolves to no message, or a non-2xx dispatch response (body truncated to ~2 KB) is a **returned error, never a panic**.
- **`ms.UserID(ctx)` and `ms.AppRole(ctx)`** — trusted identity accessors completing the read surface next to `ms.AppID` — together with `auth.Get` (the full-`Identity` read) the ONLY correct way for module code to read the request's identity: always the context, never the `X-MS-*` headers. All three resolve from the context identity the SDK promotes on every guarded surface (Platform via `PlatformAuth`, Public via the proxy guard's validated-token path, Lambda/task via the typed envelope through `runtime.InjectResources`). Reading the `X-MS-*` headers (`auth.HeaderUserID` / `auth.HeaderAppRole`) instead is the footgun these close: headers exist on the dev tunnel but the deployed Lambda shim **strips** every client-settable identity header, so header reads silently break in production — the exact bug shipped in ms-app-modules#30, now pinned by a regression test. `""` is a legitimate return (internal/system/cron/task calls carry no user; an anonymous Public request may carry an empty user id). The `auth.Header*` constants remain exported as the internal platform-to-SDK wire; their docs now say so.

## [v0.2.6] - 2026-06-20

Prepares the SDK for the production module transport. In production a module runs as a Lambda function invoked via the HTTP-shaped `LambdaRequest` envelope; this closes the one kernel gap on that receive path so a deployed module's internal/MCP auth works.

### Changed
- **`NewLambdaHandler` no longer strips the platform-auth secret headers.** The Lambda receive path strips spoofable `X-MS-*` identity *claims* (`X-MS-User-ID`, `X-MS-App-ID`, `X-MS-App-Role`) — trusted identity arrives via the typed `LambdaRequest` fields — but now **exempts the two platform-auth *secret* headers** (`X-MS-Internal-Secret`, `X-MS-Platform-Token`). Previously every `x-ms-*` header was dropped before the router ran, so a Lambda-invoked module's `InternalAuth` / `RequireProxy` middleware could never see the secret and rejected every internal/MCP call. The secrets are platform-injected credentials, not client-spoofable claims (the platform builds a fresh header set per invoke), so letting them through is safe and restores the documented `internalAuth` behavior on the Lambda path.

## [v0.2.5] - 2026-06-19

A module can now mark one of its own tables **read-only eligible** for a depending module — the producer half of the cross-module data contract. v0.2.0's design notes deferred this in favor of `pg_class` introspection; an explicit declaration is clearer (the producer's intent is in source, not inferred) and keeps the GRANT surface auditable, while preserving the same trust model: the producer marks a table readable, the **app owner** decides who reads it.

### Added
- **`ms.ExposeTable(name string)`** — a zero-runtime DECLARATION that marks a table in the module's `mod_<id>` schema as SELECT-eligible for a depending module (the producer side of `ms.DependsOn`'s `n.Table`). It surfaces in the manifest under a new top-level `exposes` block — `"exposes": { "tables": [...] }` — a flat, **sorted, de-duplicated** list of table names. The platform catalog issues `GRANT SELECT` against a depending module's DB role only after the **app owner** approves that dependency. v1 is **TABLES ONLY, read-only**. There is intentionally **no per-consumer `readableBy` allowlist**: in a marketplace the consumers are third parties, so a publisher-controlled reader list is the wrong trust model — the producer opts a table *in* to being readable, the app owner (the trust root) decides *who* reads. Repeated/feature-flagged declarations of the same name compose safely (set union); an empty or non-identifier-shaped name (`^[a-z][a-z0-9_]{0,62}$`, the Postgres NAMEDATALEN ceiling) panics at startup. The manifest always carries `exposes` (an empty `tables` array when the module exposes nothing).

## [v0.2.4] - 2026-06-17

The usage-meter transport moves from an AWS Lambda invoke to a dispatch-HTTP POST, exactly mirroring `ms.Emit`. The public metering API (`ms.Meter` declaration, `ms.Record` emit-by-name, the v1 envelope with no kind on the wire, the reserved-namespace guards) is unchanged — only how a recorded event reaches the platform changes.

### Changed
- **Meter transport is now dispatch-HTTP, not Lambda.** `ms.Record` POSTs the usage `Event` envelope to the platform dispatch usage ingress at `{MS_DISPATCH_URL | dev fallback}/apps/{appID}/usage` (the same transport idiom as `ms.Emit` / `ms.Call`), in **both dev and prod** — there is no separate dev sink. Dispatch re-derives the authoritative app/module/owner/recorded_at and forwards to billing-engine; the SDK's `Hint` fields stay untrusted. The app id comes from the request context's auth identity (`auth.Get`), and an empty app id returns an error (no panic, no HTTP call), mirroring `ms.Emit`. The transport HTTP client is built once at `meter.New()` and is never nil. The non-fatal contract is unchanged: a transport failure is returned (then logged by the caller), never propagated to fail the handler. The `EventID` is still minted once per `Record` call and reused across any transport retry, so the platform's `ON CONFLICT(event_id)` dedupe holds.

### Removed
- **`MS_METER_LAMBDA_ARN` and the AWS Lambda meter transport.** The `meter` package no longer invokes a Lambda: the `MS_METER_LAMBDA_ARN` environment variable is gone, along with `meter.NewFromARN` / `meter.NewDev` (replaced by a single `meter.New()`), the ARN-format validation, and the `github.com/aws/aws-sdk-go-v2/service/lambda` dependency. Usage transport is configured via **`MS_DISPATCH_URL`** (the same base `ms.Emit` / `ms.Call` use); `meter.New()` fail-fast validates it as a parseable http(s) base when set, so a typo surfaces at startup rather than as silently lost usage. An unset `MS_DISPATCH_URL` falls back to the local dispatch for dev.

## [v0.2.3] - 2026-06-17

Metric kind moves from a positional argument to a declaration OPTION, and a module may now override the **customer** price of a platform-infra metric. The runtime emit (`ms.Record`) and the declaration-first contract are unchanged; only the `ms.Meter` shape and the reserved-namespace rules change.

### Changed (BREAKING)
- **`ms.Meter` kind is now an OPTION, not a positional argument.** The signature is `ms.Meter(name string, opts ...ms.MetricOption)`. `ms.Counter` and `ms.Gauge` are now `ms.MetricOption`s (functional options that set the kind) rather than `ms.Kind` values, so a call reads the same — `ms.Meter("orders.placed", ms.Counter, ms.Unit("order"), ms.Price(50_000))` — but the kind is supplied positionally no longer. A **custom** (non-reserved) metric MUST pass exactly one kind option: `ms.Meter` panics if no kind is given or if both `ms.Counter` and `ms.Gauge` are passed. The exported `ms.Kind` type and the `ms.Counter`/`ms.Gauge` `Kind` constants are gone (the kind enum is now internal to the manifest/registry).

### Added
- **Platform-infra customer-price override.** A reserved `infra.*` / `platform.*` metric — previously rejected outright at declaration — may now be DECLARED with `ms.Price` **only**, to override what the module's customer is billed for that platform-measured infra (e.g. `ms.Meter("infra.compute.ms", ms.Price(0))` to absorb platform compute into the module's own pricing). This is a pure customer-facing (Plane-2) choice: the developer still owes the platform the measured COGS regardless. Passing a kind (`ms.Counter`/`ms.Gauge`) or `ms.Unit` on a reserved name panics — kind/unit are platform-owned. The manifest entry for such an override carries the price only (no kind/unit; the platform catalog supplies them).
- **`ms.Record` rejects a reserved name.** A module can declare a reserved `infra.*`/`platform.*` price-override but can never self-report its value: `ms.Record(ctx, "infra.compute.ms", …)` returns an error. The platform meters its own infra at its own chokepoint; an SDK-reported quantity for a reserved metric is never billable.

## [v0.2.2] - 2026-06-16

Declaration-first usage metering. A module DECLARES each metric once, up front, with its kind + unit + price (`ms.Meter`), then emits at runtime **by name** with a single `ms.Record` — exactly mirroring the `ms.Emits` (declare) / `ms.Emit` (emit by name) pair. There is no stored handle. The declaration flows into the manifest, so the platform's metric catalog is authoritative — a call site can never mislabel a metric's kind, and billing can populate its catalog before any event arrives.

### Changed (BREAKING)
- **`ms.Meter` is a DECLARATION with no return value.** `ms.Meter(name string, kind ms.Kind, opts ...ms.MetricOption)` declares a metric once in startup code (exactly like `ms.Emits` / `ms.RegisterPermission`) — it registers the metric as a side effect and returns **nothing** (no `*ms.Metric` handle). `ms.Kind` is `ms.Counter` (additive; the platform SUMs) or `ms.Gauge` (absolute level; the platform takes MAX or a time-weighted integral, never a SUM). Options: `ms.Unit(string)` and `ms.Price(microDollars int64)` — the per-unit **customer** price (charged as quantity × price with NO blanket markup); both optional. The old runtime accessor `ms.Meter(ctx) Meter`, its `Record`/`Gauge` methods, **and the `*ms.Metric` handle type are removed**.
- **Emit by name with `ms.Record(ctx, name, value) error`.** One package-level function, mirroring `ms.Emit`: it resolves the metric declared under `name` and hands it to the transport. The platform reads the declared kind from its manifest-fed catalog to decide SUM vs MAX/integral, so the call site never repeats the kind. Returns an error (does **not** panic) when `name` was never declared via `ms.Meter` (declaration-first, fail fast) or when the value is negative, NaN, or infinite; the non-fatal contract is unchanged (transport failures are logged, not propagated). The `EventID` is minted once per `Record` and reused across any transport retry.
- **`kind` does not travel on the wire.** The metric kind lives in the manifest/catalog, so the meter `Event` envelope carries no `kind` field and `envelopeVersion` stays **1**.

### Added
- **Manifest `metrics[]`** — each declared metric (`{name, kind, unit, price}`) appears in the module manifest so the platform populates its `metric_definitions` catalog at install/publish.
- **Reserved-namespace + duplicate guards.** `ms.Meter` panics on a duplicate metric name (two declarations would silently disagree on kind/price) or on a reserved `infra.*` / `platform.*` prefix (those are platform-measured infra metrics a module may not self-declare).

## [v0.2.1] - 2026-06-13

A module can now read its own **trusted app id** on every guarded surface — including Public routes, which previously had no identity at all.

### Added
- **`ms.AppID(ctx) string`** — the inbound twin of `ms.WithAppID`. Returns the app id from the request context's auth identity (`""` when none is set). This is the single **unspoofable** way a handler reads its own app: the SDK promotes the platform's trusted, dispatch-injected app id into the identity before the handler runs. Read this instead of pulling an app id off request data (query/body/path), which the caller controls and can forge.

### Changed
- **The proxy guard (`auth.RequireProxy`) now promotes trusted app identity on its success path.** After the platform token validates — which proves the `X-MS-*` headers were injected by dispatch, not client-forged — the guard sets `auth.Identity` (`AppID`/`UserID`/`AppRole`) from those headers before the handler runs. This closes a gap on **Public** routes: they mount only the proxy guard (not `PlatformAuth`), so `auth.Get(ctx).AppID` was always empty there and a module could not read its own app. Promotion never happens on a path that has not validated the token (standalone/inert and rejected requests don't promote), and never clobbers an identity already set (e.g. Lambda's `InjectResources`). Mirrors the prod-Lambda asymmetry that `runtime.InjectResources` already closed.

## [v0.2.0] - 2026-05-06

Phase 2 — module identity, prefix-aware schema resolution, and the cross-module data-routing contract. **Trust model: app owner is the trust root** for cross-module reads. The contributor declares nothing about who can read; the consumer declares what it wants from each dep; the catalog surfaces the pairing to the app owner at install time. Read-only by design — `GRANT SELECT` only, never write. Cross-module *writes* go through events or internal HTTP.

### Added
- **`Config.Slug`** — catalog-owned kebab-case handle (e.g. `"oauth"`). `^[a-z][a-z0-9-]{0,15}$`, max 16 chars (Postgres NAMEDATALEN budget). Optional in dev; required for publishing.
- **`ms.Need`** — opaque builder passed to DependsOn / OptionalDependOn callbacks:
  - `n.Table(name)` — request a SELECT against the dep's `mod_<id>.<name>` relation
  - `n.Event(name)` — subscribe to an event the dep emits
- **`ms.OptionalDependOn(spec, ...func(*Need)) OnEventOption`** — declare an optional dep co-located with an event handler:
  ```go
  ms.OnEvent("@anna/billing/payment", onPayment,
      ms.OptionalDependOn("@anna/billing@^1", func(n *ms.Need) {
          n.Table("invoices")
      }))
  ```
  If the dep isn't installed, the event source doesn't exist, the handler never fires — missing-dep failures are harmless.
- **`db.WithPrefix(ctx, prefix)` / `db.PrefixFrom(ctx)`** — context plumbing the platform's Lambda invoke shim uses to inject the live storage prefix from `app_<app_id>.module_install.prefix` per request. Distinct from `db.WithSchema` (search_path target) — prefix is the leading segment baked into per-app table names (`<username>_<slug>_<table>`).
- **`Dependency.Tables []string` + `Dependency.Events []string`** on the manifest. Both `omitempty`.
- **Manifest payload addition**: `slug` (`omitempty`).

### Changed
- **`ms.DependsOn(spec)` is now variadic** — second argument is `...func(*ms.Need)`. Existing one-arg calls still work unchanged.
- **Dependency IDs accept `@<owner>/<name>` shape** in addition to bare module IDs (`oauth-core`). Existing bare IDs continue to validate. `parseDepSpec` now splits at the **last** `@` so `@<owner>/<name>@<version>` parses correctly.
- **`ms.OnEvent` is now variadic** — third argument is `...OnEventOption`. Existing two-arg calls work unchanged. The first option-producer is `ms.OptionalDependOn`.
- `Module.ModuleDB` / `Module.ModuleTx` resolve their schema via a new `moduleSchemaFor(ctx)` helper. Production reads the prefix the platform injected via `db.WithPrefix`; dev/legacy falls back to `mod_<Config.ID>`. Compiled SQL stays vanilla.

### Removed
- **`ms.Needs(spec, handler)`** — removed in favor of `ms.OptionalDependOn` returning an `OnEventOption` for variadic `ms.OnEvent`. Migration: `ms.OnEvent("e", ms.Needs("dep", h))` → `ms.OnEvent("e", h, ms.OptionalDependOn("dep"))`. The new shape lets the same callback (`func(n *ms.Need)`) describe both required and optional deps uniformly.

### Design notes
There is no `ms.ExposeTable`-style contributor-side declaration in this release. The catalog can introspect `pg_class` at publish time to know what relations exist in the contributor's `mod_<id>` schema; consumer-side `n.Table("name")` requests are validated against that introspection at install time, then approved or rejected by the app owner. This avoids the publisher-allowlist trap (a contributor can't pre-list every future third-party consumer) and keeps the surface small.

## [v0.1.1] - 2026-05-05

### Changed
- **`Config.ID` length cap raised from 31 to 36 chars** (regex `^[a-z][a-z0-9_]{0,35}$`).
  Accommodates UUID-derived module IDs the CLI scaffold emits (`"m"` + 32 hex chars = 33 chars). The `"mod_"` prefix the SDK adds when constructing schema names still fits comfortably under Postgres's 63-char identifier limit.
  Migration: existing module IDs continue to validate unchanged. Only users hitting the previous 31-char ceiling are affected, and only if they bumped *up* — there is no downgrade footgun.

## [v0.1.0] - 2026-05-04

First tagged release. Establishes the public Go module path
`github.com/mirrorstack-ai/app-module-sdk` so downstream modules scaffolded
by the CLI can `require` a real version instead of leaning on a `replace`
directive into a sibling checkout.

### Added
- **Typed role values** for `ms.RequirePermission` via a new `roles` package — `roles.Admin()`, `roles.Viewer()`, `roles.Custom(key)`. Prevents typos, enables IDE autocomplete, and reserves space for future i18n metadata.
- **Agent orchestration primitives** ([#82], [#84])
  - `ms.Describe(s)` — human-readable module description consumed by the catalog for agent discovery.
  - `ms.DependsOn(spec)` — declare a REQUIRED dependency. Spec syntax is `"id"` (any version) or `"id@constraint"` with npm-style SemVer (`^1.2.0`, `~1.2.0`, `>=1.2.0 <2.0.0`, `1.x`, `1.2.3`). Constraints validated at registration — invalid ones panic immediately.
  - `ms.Needs(spec, handler) HandlerFunc` — wrap a handler to declare an OPTIONAL dependency, co-located with the code that uses it. Same spec syntax. Works with `OnEvent`, `Cron`, chi routes, any `http.HandlerFunc`.
  - `ms.Resolve[T any](id) (T, bool)` — typed runtime resolver for optional deps. v1 stub; returns zero + false until cross-module client wiring lands.
  - `ms.MCPTool[In, Out](name, description, handler)` — agent-callable tool. Input/output JSON Schema derived from type parameters via reflection.
  - `ms.MCPResource[Out](name, description, handler)` — agent-readable resource.
  - Routes under Internal scope: `/__mirrorstack/mcp/tools/list`, `/tools/call`, `/resources/list`, `/resources/read`.
- **Manifest payload additions**
  - `Description`, `Dependencies` ([#82])
  - `MCP.Tools`, `MCP.Resources` ([#84])

### Changed
- **BREAKING**: `ms.RequirePermission(name, roles ...string)` → `ms.RequirePermission(name, roles ...roles.Role)`. Migration: replace `"admin"` with `p.Admin()`, `"viewer"` with `p.Viewer()`, any other string with `p.Custom("...")`. Manifest wire shape is unchanged (role keys still serialize as strings).
- `ManifestPayload` wire shape is additive (new fields are `omitempty` or emit empty arrays rather than null).
- **Internal restructure**: All implementation moved from SDK root into `internal/core/` (module.go, db.go, describe.go, mcp.go, cron.go, event.go, task.go, resources.go). SDK root now contains only `mirrorstack.go` — a facade with type aliases and wrapper functions. **No public API change** — all `ms.*` functions work identically; this is internal-only restructuring.

### Documentation
- First `CHANGELOG.md`, `docs/` tree, and `examples/template/` module.

## Historical (pre-0.1)

Work prior to [#82] was shipped without a changelog. Grouped below by theme.

### Platform and lifecycle
- Module manifest endpoint ([#6])
- Lifecycle routes: install / upgrade / downgrade / uninstall, per-scope namespace ([#8], [#57])
- Config.ID format validation
- Per-module shared schema `mod_<id>` with disjoint DB credentials ([#31], [#55], [#56], [#58], [#66])

### Auth and permissions
- Permission registry ([#28])
- `InternalAuth` fail-fast on missing `MS_INTERNAL_SECRET` ([#36])
- Rejected internal auth requests logged ([#43])
- `MaxBytesReader` on Internal scope routes ([#52])

### Events, crons, tasks
- `ms.OnEvent` / `ms.Emits` / `ms.Cron` ([#9])
- `ms.OnTask` / `ms.RunTask` — SQS-backed task worker with HMAC signing and SIGTERM graceful shutdown ([#32], [#34])

### Data
- `ms.DB` / `ms.Tx` with per-app credentials ([#3])
- `ms.ModuleDB` / `ms.ModuleTx` with per-module `mod_<id>` credentials ([#58])
- Storage (S3 origin + presigned multipart upload, R2 + Cloudflare Worker read cache) ([#11])
- Cache ([#12])
- Meter — `ms.Meter(ctx).Record(metric, value)` via async Lambda invoke ([#13])

### Testing and DX
- Lambda env detection consolidated into `internal/lambdaenv` ([#40])
- Test migration to `newTestModuleWithSecret` helper ([#53])
- Dev-mode HTTP warning in README ([#42])
- `InternalAuth` godoc contract ([#54])

[#82]: https://github.com/mirrorstack-ai/app-module-sdk/issues/82
[#84]: https://github.com/mirrorstack-ai/app-module-sdk/issues/84
[#3]: https://github.com/mirrorstack-ai/app-module-sdk/issues/3
[#6]: https://github.com/mirrorstack-ai/app-module-sdk/issues/6
[#8]: https://github.com/mirrorstack-ai/app-module-sdk/issues/8
[#9]: https://github.com/mirrorstack-ai/app-module-sdk/issues/9
[#11]: https://github.com/mirrorstack-ai/app-module-sdk/issues/11
[#12]: https://github.com/mirrorstack-ai/app-module-sdk/issues/12
[#13]: https://github.com/mirrorstack-ai/app-module-sdk/issues/13
[#28]: https://github.com/mirrorstack-ai/app-module-sdk/issues/28
[#31]: https://github.com/mirrorstack-ai/app-module-sdk/issues/31
[#32]: https://github.com/mirrorstack-ai/app-module-sdk/issues/32
[#34]: https://github.com/mirrorstack-ai/app-module-sdk/issues/34
[#36]: https://github.com/mirrorstack-ai/app-module-sdk/issues/36
[#40]: https://github.com/mirrorstack-ai/app-module-sdk/issues/40
[#42]: https://github.com/mirrorstack-ai/app-module-sdk/issues/42
[#43]: https://github.com/mirrorstack-ai/app-module-sdk/issues/43
[#52]: https://github.com/mirrorstack-ai/app-module-sdk/issues/52
[#53]: https://github.com/mirrorstack-ai/app-module-sdk/issues/53
[#54]: https://github.com/mirrorstack-ai/app-module-sdk/issues/54
[#55]: https://github.com/mirrorstack-ai/app-module-sdk/issues/55
[#56]: https://github.com/mirrorstack-ai/app-module-sdk/issues/56
[#57]: https://github.com/mirrorstack-ai/app-module-sdk/issues/57
[#58]: https://github.com/mirrorstack-ai/app-module-sdk/issues/58
[#66]: https://github.com/mirrorstack-ai/app-module-sdk/issues/66
