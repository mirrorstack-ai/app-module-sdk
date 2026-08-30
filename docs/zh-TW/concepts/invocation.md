# 受信任的 invocation context

> Language: [English](../../concepts/invocation.md) · **繁體中文**

API Platform 每次把 request 交給 module 時,都會附上一份具版本、與 transport
無關的 invocation context。SDK 會先驗證平台 credential,再檢查 context 是否與
實際的 method、path、body 和 module 一致,移除 wire representation,最後才執行
你的 handler。

從 request context 讀取:

```go
import "github.com/mirrorstack-ai/app-module-sdk/invocation"

func handle(w http.ResponseWriter, r *http.Request) {
    trusted, ok := invocation.FromContext(r.Context())
    if !ok {
        // 獨立單元測試或 legacy caller 沒有提供 typed context。
        http.Error(w, "trusted invocation required", http.StatusUnauthorized)
        return
    }

    requestID := trusted.Request.ID
    userID := trusted.Identity.UserID
    publicBase := trusted.Routes.Public
    _ = requestID
    _ = userID
    _ = publicBase
}
```

`FromContext` 刻意是 module code 唯一能使用的 context 操作。SDK 不公開 setter
或 wire decoder,因此 module 不能把 raw header 變成看似由平台簽發的 context。
回傳值會複製 slice;修改 `Redirects` 或 `Capabilities` 不會改到 request context。

## 欄位意義

| 欄位 | 意義 |
|---|---|
| `App` / `Module` | 受信任的 installation 與 callee identity。 |
| `Identity` | 明確區分 `member`、`platform` 或無 actor,並帶 platform role/delegation。 |
| `Routes` | Canonical origin、module/public/platform path,以及 redirect array。 |
| `Request` | Occurrence ID、method、local path、body digest 與發生時間。 |
| `Trust` | Edge 推導出的 source、可用時的 client IP、host、scheme 與 origin。 |
| `Cookies` | 平台擁有的 cookie namespace 與 migration capability。 |
| `Audit.Provenance` | 交給 audit ingress 的 opaque、occurrence-bound 平台證據。 |

一般 authorization 繼續使用 `ms.UserID`、`ms.AppID`、`ms.AppRole` 與
`ms.RequirePermission`。需要 identity namespace、canonical routes、redirects、
trusted connection facts、request occurrence、cookie capability 或 audit
provenance 時,才讀完整 invocation context。

## Trust 規則

- 不要讀 `X-MS-Invocation` 或 legacy `X-MS-*` identity headers。SDK 會消耗並
  移除它們;handler 不應依賴 transport representation。
- 不要自行組 physical cookie name。Module 繼續讀寫 logical cookie name;
  API Platform 會把它映射到所屬 app/module 的 namespace。
- 把 `Audit.Provenance` 視為 opaque。原樣交給平台 audit ingress;不要 decode、
  verify、修改或寫進 log。
- `Trust.ClientIP` 可能為空。不要自行猜測,也不要 fallback 到未受信任的
  forwarding header。
- 在獨立單元測試與 bounded legacy migration 期間,`FromContext` 可能不存在。
  需要 production trust 的測試應拒絕缺值或走 SDK transport adapter,不要自行
  製造 context。

Direct HTTP、WSS/local relay 與 deployed Lambda 會消耗同一份 canonical v1
bytes。Migration 期間平台可在 2026-11-30 前 dual-send legacy claims;只要 legacy
值與 typed context 衝突,SDK 就會在 handler 執行前拒絕 request。
