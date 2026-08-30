# 可持久化的 audit record

> Language: [English](../../concepts/audit.md) · **繁體中文**

需要回答「誰從哪個已認證 request，對哪個 subject 做了什麼」的 business
change，請使用 SDK audit outbox。不要建立自訂 audit table，也不要轉送來自
route、header、query、body 或 module field 的 actor。

責任邊界固定如下：

- module 只描述發生的事情：subject kind、subject id、action，以及小型結構化 details；
- API Platform 從原始 signed invocation 證明 app、module、actor、request 與連線 context。

## 在 mutation transaction 內記錄

把 mutation 使用的同一個 querier 傳給 `audit.Record`：

```go
err := ms.Tx(r.Context(), func(q db.Querier) error {
    if _, err := q.Exec(r.Context(),
        `UPDATE users SET display_name = $1 WHERE id = $2`, name, userID,
    ); err != nil {
        return err
    }
    return audit.Record(r.Context(), q, audit.Entry{
        SubjectKind: "user",
        SubjectID:   userID,
        Action:      "updated",
        Details:     map[string]any{"field": "displayName"},
    })
})
```

`audit.Record` 寫入目前 app schema 內由 SDK 擁有的 table。Commit 會同時保留
business change 與 audit row；rollback 兩者都不保留。Lifecycle provisioner
會建立與升級此 table，所以 module 不需要 audit migration。

`Entry` 刻意沒有 actor、app、module、request、invocation 或 provenance field。
也不要把這些值藏進 `Details`；details 是 business context，不是可信任的 identity。

## 必須有 typed invocation

SDK 在驗證 request 時，會私下保留 API Platform 送來且通過驗證的 canonical
invocation bytes。`audit.Record` 將其原樣寫入 outbox。Module code 可以透過
`invocation.FromContext` 讀取 typed projection，但不能設定或讀取 private proof。

若 context 不是由 authenticated typed transport 建立，`audit.Record` 會在執行
任何 SQL 前回傳 `audit.ErrProvenanceUnavailable`。這在 proofless standalone test
或 legacy caller 是預期行為。Audited mutation 的測試應經過 SDK request adapter，
不得自行製造 trusted context。

請把此錯誤從 transaction 往外回傳。若 business mutation commit、audit row 卻
不存在，聲稱完整的 trail 就會產生缺口。

## Delivery 與復原

成功且呼叫過 `audit.Record` 的 `ms.Tx` commit 後，SDK 會執行一次有界、
best-effort 的 drain。沒有寫 audit event 的 transaction 不會多做 query。若 API
Platform 暫時不可用，已 commit 的 mutation 不會被回報為失敗；outbox row 仍會
持久保存並可重試。

Backlog worker 或 scheduled maintenance handler 可呼叫：

```go
err := ms.DrainAudit(ctx)
```

每次最多 claim 一個有界 batch。Claim 使用 `SKIP LOCKED`、會過期的 lease 與
每次更新的 fence token，所以多個 worker 可以安全並行。穩定 event id 讓完全
相同的 replay 在 API Platform 冪等；相同 id 搭配不同內容則是 conflict。

Module 不擁有 delivery SQL、HTTP header、endpoint path、backoff 或 credential
refresh。`ms.DrainAudit` 從目前 execution context 取得 renewable module authority，
而 stored invocation proof 只會在 platform ingress boundary 被還原。

## Failure 行為

| Failure | 結果 |
|---|---|
| 欄位缺漏、details 過大/不可序列化、或缺少 typed proof | `audit.Record` 在 mutation transaction 內失敗。 |
| Outbox insert 失敗 | Mutation transaction 失敗。 |
| Transaction rollback | 不留下 audit row，也不執行 drain。 |
| Network、authority、rate limit 或暫時性 platform failure | 已 commit row 以有界 backoff 釋放並重試。 |
| 永久無效 event（`400`、`409`、`413`、`422`） | Row 被 quarantine 供檢查，永不刪除。 |
| Retry budget 用盡 | Row 帶 bounded failure code 被 quarantine。 |
| 舊 SDK row 沒有 invocation proof | Upgrade 標記為 `missing_invocation_proof`；SDK 不會虛構 provenance。 |

## Security rules

- 不得 decode 或 log `X-MS-Invocation`、`Audit.Provenance`、stored proof、service
  credential 或 audit ingress response body。
- 不得透過 `audit.Entry` 或 `Details` 接受 actor 或 trusted scope。
- `audit.Record` 必須使用原始 request context，不要換成 `context.Background()`。
- `Details` 保持小型且結構化；不得存放 credential、session token、raw request
  body 或非必要個資。
- 重複發生的 business fact 使用 `audit.Record`，不要建立 module-specific ledger。

## Final-shape checklist

- [ ] Mutation 與 `audit.Record` 使用同一個 `ms.Tx` querier
- [ ] `Entry` 只有 subject kind/id、過去式 action、bounded details
- [ ] 沒有自訂 audit migration、actor field、outbox drainer 或 HTTP transport
- [ ] `audit.ErrProvenanceUnavailable` 會被往外回傳
- [ ] Backlog 使用 `ms.DrainAudit`，不複製 SDK mechanics
- [ ] 需要 provenance 的 unit test 經過 authenticated SDK transport

另見 [trusted invocation context](invocation.md)、[scopes](scopes.md) 與
[module structure](module-structure.md)。
