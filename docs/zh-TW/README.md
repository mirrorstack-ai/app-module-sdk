# MirrorStack Module SDK — 文件

> Language: [English](../README.md) · **繁體中文**

用 Go 寫 MirrorStack module 的 reference 和概念介紹。

## 開始

- [Getting started](./getting-started.md) — 用 template 建你第一個 module。

## 核心概念

- [Agent tools](./concepts/agent-tools.md) — MCP tools/resources vs skills vs subagents;為什麼 module 是 agent-first。
- [Dependencies](./concepts/dependencies.md) — required vs optional deps、auto-detect 規則、extract-function 的坑。
- [Manifest](./concepts/manifest.md) — manifest endpoint 裡有什麼,每個 field 的用途。
- [Scopes](./concepts/scopes.md) — Platform / Public / Internal 三種 scope 什麼時候用哪個。

## Reference

- [API reference](./api-reference.md) — 每個 `ms.*` function 搭配一行 example。

## 其他

- [Template module](../../examples/template/) — CLI 會 pull 的 scaffold。
- [Changelog](../../CHANGELOG.md)。
