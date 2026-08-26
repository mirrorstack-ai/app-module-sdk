# MirrorStack Module SDK — 文件

> Language: [English](../README.md) · **繁體中文**

以 Go 開發 MirrorStack module 的 reference 與概念介紹。

## 開始

- [Getting started](./getting-started.md) — 用 template 建立你的第一個 module。

## 核心概念

- [Module structure](./concepts/module-structure.md) — 每個已發布模組共用的骨架，以及決定檔案該放哪個目錄的那一個問題。
- [Agent tools](./concepts/agent-tools.md) — MCP tools 與 resources 的差別,以及為什麼 module 是 agent-first。
- [Dependencies](./concepts/dependencies.md) — Required 與 optional 相依、SemVer version constraint、 `ms.Needs` 用法。
- [Manifest](./concepts/manifest.md) — Manifest endpoint 的完整欄位與用途。
- [Scopes](./concepts/scopes.md) — Platform / Public / Internal 三種 scope 的選用時機。

## Reference

- [API reference](./api-reference.md) — 每個 `ms.*` function 搭配一行範例。

## 其他

- [Template module](../../examples/template/) — 完整的範例模組：cache、events、
  meter、notifications、schedule、storage 與 tasks 都在同一處。
  這**不是** `mirrorstack module init` 產生的內容——CLI 使用自己維護的精簡
  範本（`templates/module/`）。想看 API 怎麼用就讀這份；要開新專案請用 CLI。
- [Changelog](../../CHANGELOG.md)。
