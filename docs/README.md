# MirrorStack Module SDK — Docs

> Language: **English** · [繁體中文](./zh-TW/README.md)

Reference + conceptual guides for building MirrorStack modules in Go.

## Getting started

- [Getting started](./getting-started.md) — build your first module using the template.

## Concepts

- [Module structure](./concepts/module-structure.md) — the one skeleton every shipped module uses, and the single question that decides which directory a file goes in.
- [Agent tools](./concepts/agent-tools.md) — MCP tools/resources vs skills vs subagents; why modules are agent-first.
- [Durable audit records](./concepts/audit.md) — atomic module-owned facts with platform-proven actor/request provenance and shared outbox delivery.
- [Dependencies](./concepts/dependencies.md) — required vs optional deps, auto-detect rule, extract-function caveat.
- [Manifest](./concepts/manifest.md) — what's in the manifest endpoint, field by field.
- [Scopes](./concepts/scopes.md) — Platform / Public / Internal and when to use each.
- [Trusted invocation context](./concepts/invocation.md) — the read-only, transport-neutral request contract installed after platform authentication.

## Reference

- [API reference](./api-reference.md) — every `ms.*` function with one-line examples.

## Related

- [Template module](../examples/template/) — a complete worked module: cache,
  events, meter, notifications, schedule, storage and tasks in one place.
  It is NOT what `mirrorstack module init` writes — the CLI vendors its own
  minimal templates under `templates/module/`. Read this one to see an API
  in use; run the CLI to start a project.
- [Changelog](../CHANGELOG.md).
