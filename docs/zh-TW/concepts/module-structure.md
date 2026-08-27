# 模組結構

> 語言：[English](../../concepts/module-structure.md) · **繁體中文**

每個已發布的 MirrorStack 模組都採用相同骨架。**11 個模組全部遵循它**，但在此之前
它只被寫在一個地方——某個模組自己的 `CLAUDE.md`——所以新作者只能從鄰近模組逆向推
導，而 `mirrorstack module init` 產生的又是完全不同的東西。這份文件就是那條規則。

## 分界

只有一個問題，它決定檔案該放在哪個目錄：

> **這段程式碼會掛載路由，或持有 `http.HandlerFunc` 嗎？**
>
> - **否** — 純資料宣告 → `declare/`
> - **是** — 每次請求都會執行 → `internal/handlers/`

`declare/` 在啟動時、`ms.Start()` 之前變更 SDK registry 一次。
`internal/handlers/` 在那之後持續回應請求。`declare/` 裡不應出現 `*http.Request`，
`internal/handlers/` 裡也不應呼叫 `ms.RegisterPermission` 或 `ms.ContributesTo`。

這個切分不是美學問題。宣告集合會成為 **manifest**，而平台正是讀取 manifest 來決定
一個模組被允許做什麼——安裝前置條件、權限預設值、可讀取的資料表、contribution
slots。把它放在看不到請求的檔案裡，才使它成為關於「模組」的陳述，而不是關於「某一
個呼叫者」的陳述。

## 目錄樹

```
my-module/
├── main.go                     ms.Init(Config) → RegisterMessages → declare.Register()
│                               → handlers.Register() → ms.Start()
├── go.mod
├── CHANGELOG.md                每個版本一個 `## <version>` 段落；部署時會硬性檢查
├── declare/
│   ├── register.go             Register()：main.go 呼叫的唯一進入點
│   ├── permissions.go          ms.RegisterPermission — 名稱與預設角色
│   ├── ui.go                   ms.RegisterUI — 導覽項目與頁面
│   ├── events.go               ms.Emits / ms.OnEvent 宣告
│   ├── expose.go               ms.ExposeTable — 提供給依賴者的唯讀表面
│   ├── contracts.go            ms.ContributesTo 的共用 ref 與 payload 型別
│   └── meter.go                ms.RegisterMetric
├── internal/
│   ├── handlers/
│   │   ├── routes.go           Register()：ms.Public / ms.Platform / ms.Internal 區塊
│   │   ├── mcp.go              ms.MCPTool 宣告與其 handler
│   │   └── <feature>.go        一個功能一個檔案，以它服務的對象命名
│   └── sqlc/                   產生的查詢（sqlc.yaml 放在模組根目錄）
├── i18n/
│   ├── en-US.json              權限標籤、導覽文案、工具描述
│   └── zh-TW.json              與 en-US.json 逐鍵對齊
├── sql/app/
│   └── 0001_init.up.sql        per-app migration；已發布的編號永不重編
└── web/                        選用的管理介面；Config.WebDir 指向 web/dist
```

只有 `main.go`、`go.mod`、`CHANGELOG.md`、`declare/` 與 `sql/app/` 是必要的。
其餘在模組需要時再加。

## 兩條不那麼明顯的規則

**`main.go` 永遠不會變長。** 它讀取 `Config`、載入 i18n、呼叫兩個 `Register()`
然後啟動。新增功能永遠不需要動它——這正是 `declare.Register()` 與
`handlers.Register()` 作為單一進入點存在的理由，而不是一堆 `init()`。以 `init()`
建立 registry 看起來更整潔，代價是失去確定性的順序：i18n catalog 必須在權限標籤
解析**之前**載入，而 `init()` 沒有辦法表達這件事。

**永遠不要把檔案命名為 `handlers.go`。** 那是你還沒想清楚這段程式碼是什麼時會用的
名字，也正是 `user-core/internal/handlers/handlers.go` 長到 1038 行、同時裝著
sessions、admin、profile 與 metering 的原因。用它服務的表面來命名——`sessions.go`、
`profile.go`、`metering.go`——那麼你早晚要做的拆分，其實一開始就做完了。

## 延伸閱讀

- [Scopes](./scopes.md)
- [Manifest](./manifest.md)
- [Agent tools](./agent-tools.md)
