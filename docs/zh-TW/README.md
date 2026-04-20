# MirrorStack Module SDK — 文件

> Language: [English](../README.md) · **繁體中文**

以 Go 開發 MirrorStack module 的 reference 與概念介紹。

## 開始

- [Getting started](./getting-started.md) — 用 template 建立你的第一個 module。

## 核心概念

- [Agent tools](./concepts/agent-tools.md) — MCP tools 與 resources 的差別,以及為什麼 module 是 agent-first。
- [Dependencies](./concepts/dependencies.md) — Required 與 optional 相依、SemVer version constraint、 `ms.Needs` 用法。
- [Manifest](./concepts/manifest.md) — Manifest endpoint 的完整欄位與用途。
- [Scopes](./concepts/scopes.md) — Platform / Public / Internal 三種 scope 的選用時機。

## Reference

- [API reference](./api-reference.md) — 每個 `ms.*` function 搭配一行範例。

## 其他

- [Template module](../../examples/template/) — CLI 會據此複製的 scaffold。
- [Changelog](../../CHANGELOG.md)。
