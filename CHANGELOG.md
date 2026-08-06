# Changelog

All notable changes to the MirrorStack Module SDK.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [v0.4.0] - 2026-08-06

This release makes module storage an explicit, fail-closed resource and makes
contribution slots available on both the dev and deployed request planes. It is
a minor release because the exported lifecycle handler constructors now accept
the contribution registry used to provision the per-app contribution store.

### Added

- **Modules must declare storage with `ms.RequireStorage()`.** The declaration
  is emitted as `resources.storage` in the manifest, giving the platform an
  auditable gate before it vends credentials. `ms.Storage(ctx)` returns
  `storage.ErrNotDeclared` when the declaration is absent; there is no
  undeclared fallback.
- **Dev storage now has production-shaped isolation.** The SDK exchanges a
  non-root MinIO parent identity for a short-lived STS session scoped to exactly
  `apps/<appUUID>/<moduleUUID>/`. Concurrent callers share one bounded,
  single-flight credential cache, and the SDK never falls back to AWS or an
  unscoped prefix when local STS is unavailable.
- **Storage credentials carry `expiresAt`.** Put/get/multipart presigns clamp
  their lifetime below the credential expiry with a safety margin, and expired
  credentials fail with `storage.ErrCredentialExpired` rather than producing a
  URL that will fail later.

### Changed

- **`ms.Storage(ctx)` is plane-neutral and always prefix-scoped.** Deployed
  requests consume the platform-injected resource; dev requests mint the same
  bounded shape locally. Compatibility helpers such as `storage.Open` and
  `Client.ForApp` remain available but cannot widen or replace the vended
  app/module prefix.
- **Lifecycle handler constructors accept the contribution registry.**
  `system.InstallHandler` and `system.UpgradeHandler` now receive the registry
  needed to provision the module's contribution table inside the target app
  schema. Normal modules using `ms.Start()` require no call-site change;
  direct users of these exported constructors must pass the registry.
- **UI modules opt in to local-network access.** Tunnel-served bundles can load
  their local development assets without weakening deployed origins.

### Fixed

- **Contribution stores are created on deployed install/upgrade and heal only
  a missing-table error.** App-schema isolation, advisory locking, and bounded
  concurrent provisioning are shared with the dev lifecycle. Auth-provider and
  role-assignment contributions no longer depend on a dev-only table side
  effect.
- **`ms.Emit` authenticates with the module service secret**, matching the
  already authenticated usage and notification callbacks.
- **`ms.Call` authenticates inter-module requests with the module service
  secret.** Dispatch can now distinguish a module caller from a browser caller
  before it injects the bounded internal identity, and the credential is
  omitted when it is not configured and never included in returned errors.
- **A declared UI without a bundle fails startup** instead of advertising a
  surface that can only 404 at runtime.
- Multipart uploads can be aborted through the same exact-prefix resource
  contract used for their parts.

### Upgrade notes

- Add `ms.RequireStorage()` during module registration before calling
  `ms.Storage(ctx)`. A module that used implicit environment-based storage will
  now fail closed until it declares the resource.
- Deployed storage requires the paired api-platform resource-vending release;
  upgrade the platform before rebuilding storage-using modules on v0.4.0.
- Direct callers of `system.InstallHandler` or `system.UpgradeHandler` must pass
  the module contribution registry as the new argument.
- Rebuild every storage-using module on v0.4.0; older SDKs do not enforce the
  declaration or credential-expiry boundary.

Includes app-module-sdk #163, #164, #165, #166, #167, and #170. Part of
mirrorstack-ai/mirrorstack-core-v2#338.

## [v0.3.2] - 2026-08-02

**No wire change. Nothing about what this SDK sends is different from v0.3.1** — this release documents and test-pins a contract that already holds, so that the platform can start authenticating *deployed* modules without any module rebuild.

### Added
- **The deployed-plane callback contract is now pinned by tests.** A deployed module (a provisioned Lambda, no `mirrorstack dev --tunnel` session) calls back into dispatch for usage ingest and notifications, and dispatch must be able to authenticate it as that specific module. That credential arrives the same way the tunnel one always has — as `MS_INTERNAL_SECRET`, forwarded as `X-MS-Service-Secret` — except that on the deployed plane the deploy provisioner injects a **per-module** value rather than the CLI injecting a per-session one. Dispatch constant-time **byte**-compares it against a value it recomputes independently, so the SDK forwarding it verbatim is a hard requirement: trimming, case-folding, re-encoding or prefixing it would produce an unattributable 403, and because metering is non-fatal by contract that 403 is **silent** — usage would simply stop being billed with nothing in the module's own logs. `ms.Record` and `ms.Notify` now each assert verbatim single-valued forwarding of a realistic opaque credential.
- **The credential is pinned as un-loggable.** `ms.Record`'s error is exactly what a module logs on the non-fatal metering path, so leaking the credential into it would publish a module's callback credential straight into CloudWatch. Both the non-2xx and the transport-failure error paths of `ms.Record`, and the non-2xx path of `ms.Notify`, now assert the credential does not appear in the returned error.

### Changed
- **`resolveUsageURL`'s "swap the body of this function when #146 lands" instruction is resolved and removed — the answer was that no swap is needed.** #146 (the production module→dispatch transport) turned out to require no SDK code at all: the deployed leg is the same `MS_DISPATCH_URL` base the dev leg uses, and the deploy provisioner injects that variable into every module Lambda. The comment is replaced with the reason, plus an explicit warning **not** to repoint the `host.docker.internal:8083` fallback at production — that fallback is what makes a full local platform stack work, and defaulting it to prod would make a local stack silently emit usage against the real dispatch. `resolveNotifyURL` carried the same now-obsolete `#146` marker and gets the same treatment.
- **`moduleSessionSecret` is documented as no longer tunnel-only.** It now carries either credential depending on plane. The function name is deliberately unchanged: renaming it would churn every call site for zero behavioral gain.
- **`ms.DependencyDB`'s read-exposed proxy is documented as staying tunnel-only, by decision.** It is the one module→dispatch surface *not* widened to accept the per-module credential, and its "run under `mirrorstack dev --tunnel`" error correctly stays: the deployed plane cannot reach that code path (a trusted-payload invoke routes to `resultDeployed`), because a deployed consumer reads a producer through the platform-injected `Dependencies` manifest under the install-time GRANT ceiling, never through read-exposed.

### Upgrade notes
- **Nothing forces you to upgrade, and upgrading changes no behavior.** A module still on v0.3.1 authenticates on the deployed plane exactly as one on v0.3.2 does. Modules must still be on **at least v0.3.1** — v0.3.0 sends no credential at all and 403s on usage and notifications.
- **Ordering lives entirely on the platform side.** Dispatch must accept the per-module credential *before* the provisioner starts injecting it; otherwise the first provisioned module 403s on every `ms.Record`/`ms.Notify`. This SDK release is not part of that ordering.
- The per-module credential's isolation guarantee is conditional on dispatch no longer shipping the platform secret to modules in the invoke envelope (`X-MS-Internal-Secret`), which is tracked separately.

Part of mirrorstack-ai/mirrorstack-core-v2#338 (step 6). Closes core-v2#337 defect 1 together with the platform side.

## [v0.3.1] - 2026-08-02

### Fixed
- **The meter and `ms.Notify` now send the module's tunnel-session secret.** Dispatch's `POST /apps/{appID}/usage` and `/apps/{appID}/notifications` are mounted outside all auth middleware and used to gate the sender on session *existence* alone, so an unauthenticated caller on the open internet could POST forged billable usage events for any app with a tunnel-live module. api-platform now requires `X-MS-Service-Secret` to constant-time-match the live session's `InternalSecret` (the same gate module-log ingest and read-exposed already used); this SDK never sent that header on either transport.

  The value is the one the CLI already mints per `mirrorstack dev --tunnel` session, injects into the module process env, and sends on the WS `register` frame — so both ends carry the same secret by construction and nothing new needs configuring. `ms.Emit` and `ms.Call` are deliberately unchanged: the credential is opted into per call site rather than blanket-applied by the shared `postDispatchJSON`, so no surface acquires the session secret by accident.

  An unset secret stays best-effort — no header, no early error, the request is still sent — because metering must never break a module's own request path, and dispatch is the fail-closed enforcement point. That is intentionally unlike `ms.DependencyDB`, which hard-errors.

  ⚠️ **Upgrade ordering matters.** api-platform's side of this is already merged, so a module still on **v0.3.0 will 403 on usage and notifications** once that reaches an environment. Bump to v0.3.1 and rebuild your modules **before** the platform deploys. A CLI old enough not to send `internal_secret` on its register frame is also now rejected on these two routes, as it already was on read-exposed and log ingest.

  Part of mirrorstack-ai/mirrorstack-core-v2#337. Pairs with api-platform#438 — the two must land together.

## [v0.3.0] - 2026-07-30

Cross-module reads resolve **locally** when the producer runs in the same `mirrorstack dev --tunnel` session. The premise the dev plane was built on — "a dev module holds no socket to the platform DB" — is true of a *remote* producer and false of a *co-located* one: under `mirrorstack dev` the producer and the consumer are two processes sharing one Postgres and one app schema. Routing a co-located read over the platform proxy therefore returned the producer's **production** rows to a consumer whose own rows are local, which is wrong data rather than slow data.

### Added
- **Co-located dev reads resolve locally.** Under `mirrorstack dev --tunnel`, `ms.DependencyDB(...).Select(...)` against a producer running in the *same* session reads that producer's tables directly from the local Postgres — same sanitized dynamic SELECT, same `READ ONLY` transaction, same SQLSTATE-to-sentinel mapping the deployed plane uses, and the same JSON-shaped row values the proxy returns (a `uuid` column arrives as a canonical `8-4-4-4-12` string, not as pgx's raw 16-byte form, at any nesting depth including `uuid[]` and jsonb/composite values). **Strictly additive**: a producer absent from the directory keeps the read-exposed proxy path byte-for-byte unchanged, so a genuinely remote producer, an unauthenticated session, and a deployed invoke all behave exactly as before. Discovery rides a dev-only `public.ms_dev_module_directory` table each module publishes itself into at boot — the database is the carrier because `mirrorstack dev` clears the child environment down to four keys and `MS_LOCAL_DB_URL` is the only guaranteed cross-module channel among them. A directory row is a **lease**, not a record: it is heartbeated while the process lives, dropped on a clean shutdown, and ignored once it ages out, so "the directory says co-located" means "co-located *right now*" and a producer you stopped degrades to the proxy instead of silently serving its abandoned tables.
- **Cross-plane parity is now stated where you can see it.** The first co-located read of each producer logs one line naming the gaps, and `ms.ErrNotExposed`'s doc comment carries the same warning — see below for what they say.
- **A contribution slot now publishes the SHAPE of its payload, not just its name.** `ms.Provide[T](key)` derives the full recursive JSON Schema of `T` and the manifest carries it at `provides[].payload`. The slot's real validator is a closure built where `T` is in scope, so it has only ever existed inside the host's own process: a contributor could discover that `user-detail-blocks` exists and still have no way to learn it wants `{title, bodyUrl}` short of reading the host's source — which a third-party author cannot do at all. Cyclic payload types are emitted through `$defs` rather than inlined, because inlining them recurses until the module **stack-overflows at boot**; the schema is also byte-stable across derivations so a redeployed manifest diffs clean.

### Changed
- **`ms.ContributesTo` takes a versioned host spec — `id` or `id@constraint`, the same form `ms.DependsOn` already takes.** `ms.ContributesTo("oauth-core", …)` was the one cross-module reference in the SDK that carried no version, even though a contribution couples you to a host's declared slot surface exactly as a dependency couples you to its API, and a host may reshape or remove a slot on any version bump. The parsed range rides the manifest at `contributesTo[].constraint` (omitted when unpinned, as for `DependsOn`) so the catalog can refuse a contribution whose range excludes the host version actually installed in the app. A malformed host id or SemVer range now panics at startup instead of shipping a manifest entry nothing can resolve. **Existing calls keep compiling**; pin them — an unpinned contribution still means "any version", which is what let a contribution outlive the slot it targets.
- **An empty projection is now proxy-only; a co-located dev read requires `Columns(...)`.** This **narrows a documented dev-plane contract**: `Select("users").Rows(ctx)` with no `Columns(...)` previously worked on the dev plane, because the proxy expands an empty projection to all visible columns. That expansion is an `information_schema` probe run *through the restricted consumer role*, which is what makes it an authorization signal at all; locally the SDK holds a superuser pool, so the identical probe would see every column and mean nothing — it would silently widen the surface and hand back columns that have no GRANT in production. A co-located read with no projection is therefore a hard error naming the fix, and matches the deployed plane, which has always required an explicit projection. Add `Columns(...)` — the same call already works on both planes.

- **A co-located read fails closed if the producer has not yet been touched for that app in this session.** Dev app schemas are provisioned lazily and *independently per module* — each module creates its own tables in `app_<id>` on its first request for that app — so a consumer that runs before the producer has ever served a request for the same app now gets `ms.ErrDependencyUnavailable` (SQLSTATE `42P01`), where the proxy previously returned the producer's **production** rows. The error names the schema, the relation and the fix (send one request to the producer for that app, then retry). This is the intended fail-closed behavior — wrong-plane rows are worse than an error — but it is the one newly-reachable failure an upgrading dev session is most likely to meet first.

### Fixed
- **Credential-keyed DB pools and Redis clients are no longer constructed twice when a same-key cache miss straddles a completed singleflight.** Singleflight coalesces callers only while the first factory call is still in flight: a goroutine could miss the cache, be descheduled until that flight had inserted its value and finished, then enter a new flight and run the expensive factory again for a key that was already cached. Besides wasting a 50–200 ms connection/TLS setup, the second result replaced the first cache entry while callers still held references to it, orphaning the original resource and making their release closures decrement the replacement's refcount instead. The slow path now rechecks the cache *inside* the singleflight closure and claims the existing entry when the earlier flight won the race, so every caller receives the cached resource and contributes exactly one reference.
- **An unset `MS_DISPATCH_URL` no longer fails mute behind the local dispatch fallback.** The fallback remains `host.docker.internal:8083` — changing it to production would make a genuinely local platform stack silently talk to prod — but it is only reachable when a full local stack is running; a `mirrorstack dev --tunnel` session has no dispatch listening there. The SDK now warns once per process when `ms.Call`, `ms.Emit` or `ms.Notify` resolves through that fallback, naming the dead-by-default address and the missing prerequisite instead of leaving the module to report an unexplained connection refusal. `ms.Call` transport errors also include the HTTP method and fully resolved URL, so an upstream handler forwarding the failure as a 502 preserves the destination that actually failed.

### Known parity gaps (dev vs. production)
These are inherent to a dev session having no install lifecycle. Both are logged at runtime, but they change how you should read a green dev run:
- **App-owner consent is NOT enforced in dev.** Locally, *declaring* `ms.DependsOn(..., n.Table(...))` plus the producer's `ms.ExposeTable` is sufficient to read. In production those two are necessary but never sufficient — the app owner must also have approved the dependency at install time. A consumer that reads cleanly all the way through dev can still be refused on install with `ms.ErrNotExposed`. Treat that error as a live runtime outcome, not a setup mistake.
- **No version anchoring in dev.** Production anchors exposure to the version the producer provably runs in that app (install pin, else serving deploy — never latest-published). Dev has one version: your working tree. Adding an `ms.ExposeTable` grants a local read on the producer's next boot; in production it takes effect only after publish **and** the app moving to that version.

## [v0.2.7] - 2026-07-15

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
