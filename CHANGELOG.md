# Changelog

All notable changes to the MirrorStack Module SDK.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [v0.2.0] - 2026-05-06

Phase 2 SDK surface — module identity, prefix-aware schema resolution, and the cross-module data-routing contract (declarative half).

### Added
- **`Config.Slug`** — catalog-owned human-readable handle (e.g. `"oauth"`). Mutable via catalog UI; renames force a new published version. Validated as kebab-case (`^[a-z][a-z0-9-]{0,15}$`, max 16 chars). Optional at `New()` so dev iteration works before the CLI scaffold has assigned one — the publish pipeline is where slugs become required. See `docs/module-identity-and-storage-prefix.md` for the rename-safety design.
- **`ms.ExposeTable(name)`** — declare a relation in this module's `mod_<id>` schema as part of its public READ API. Read-only by design — `GRANT SELECT` only, never INSERT/UPDATE/DELETE. Cross-module *writes* go through events or internal HTTP endpoints; the SDK doesn't have a write-grant surface. Despite the name, the call works for any Postgres relation kind (table, view, materialized view) since `GRANT SELECT` applies uniformly. First-wins on duplicate names.
- **`ms.DependsOn(spec, ...func(*ms.Dep))`** + **`ms.Needs(spec, h, ...func(*ms.Dep))`** — variadic callback form. The callback receives a `*ms.Dep`; call `d.Reads(name)` per relation requested from the dependency. The catalog validates each name against the dep's manifest at install time, then emits one `GRANT SELECT` per entry against this module's per-app DB role after app-owner approval.
  ```go
  ms.DependsOn("@anna/oauth@^0.4.0", func(d *ms.Dep) {
      d.Reads("oauth_users")
      d.Reads("recent_orders")
  })
  ```
  Repeated declarations with the same dep ID merge `Reads` as a set union — feature-flagged additions compose safely without dropping previously-requested relations.
- **`db.WithPrefix(ctx, prefix)`** / **`db.PrefixFrom(ctx)`** — context plumbing the platform's Lambda invoke shim uses to inject the live storage prefix from `app_<app_id>.module_install.prefix` per request. Distinct from `db.WithSchema` (search_path) — prefix is the leading segment baked into per-app table names (`<username>_<slug>_<table>`).
- **Manifest payload additions**: `slug` (`omitempty`), `exposures` (always present, possibly empty), `dependencies[].reads` (`omitempty`).

### Changed
- **`ms.DependsOn(spec)` / `ms.Needs(spec, h)` are now variadic** — second argument is `...func(*ms.Dep)`. Existing one-arg calls still work unchanged. **No breaking change** for callers that didn't supply a callback.
- **Dependency IDs accept `@<owner>/<name>` shape** in addition to bare module IDs (`oauth-core`). Existing bare IDs continue to validate. This is the catalog-published handle shape — modules referencing newly-published deps will use it.
- `parseDepSpec` now splits at the **last** `@` so `@<owner>/<name>@<version>` parses correctly. Bare-id usages (`oauth-core@^1`) are unchanged because they have only one `@`.
- `Module.ModuleDB` / `Module.ModuleTx` resolve their schema via a new `moduleSchemaFor(ctx)` helper. In production the helper returns the prefix the platform injected via `db.WithPrefix`; in dev/legacy it falls back to `mod_<Config.ID>`. Compiled SQL stays vanilla — no per-version schema literals get baked into the binary.
- `ManifestPayload` wire shape gains the `slug`, `exposures`, and per-dep `reads` fields. Existing consumers that ignore unknown fields continue to deserialize cleanly.

### Design notes
The trust model: **app owner is the trust root** for cross-module reads. The contributor declares only the public READ surface (no consumer allowlist). The consumer declares which relations they want. The catalog surfaces the pairing to the app owner at install time, who approves or rejects. There is no publisher-controlled allowlist — that doesn't fit a marketplace where third-party consumers show up after publish. See `docs/module-identity-and-storage-prefix.md` for the full rationale.

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
