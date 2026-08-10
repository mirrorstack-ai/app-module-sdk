# MirrorStack Module SDK — Docs

> Language: **English** · [繁體中文](./zh-TW/README.md)

Reference + conceptual guides for building MirrorStack modules in Go.

## Getting started

- [Getting started](./getting-started.md) — build your first module using the template.

## Concepts

- [Agent tools](./concepts/agent-tools.md) — MCP tools/resources vs skills vs subagents; why modules are agent-first.
- [Audit trails](./concepts/audit.md) — recording who changed what and how; ledger vs provenance columns, and why the actor is never a header.
- [Dependencies](./concepts/dependencies.md) — required vs optional deps, auto-detect rule, extract-function caveat.
- [Manifest](./concepts/manifest.md) — what's in the manifest endpoint, field by field.
- [Scopes](./concepts/scopes.md) — Platform / Public / Internal and when to use each.

## Reference

- [API reference](./api-reference.md) — every `ms.*` function with one-line examples.

## Related

- [Template module](../examples/template/) — working scaffold the CLI pulls from.
- [Changelog](../CHANGELOG.md).
