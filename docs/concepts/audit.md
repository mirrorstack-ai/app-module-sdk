# Durable audit records

> Language: **English** · [繁體中文](../zh-TW/concepts/audit.md)

Use the SDK audit outbox for a business change that must answer who did what,
to which subject, and from which authenticated request. Do not create a custom
audit table or forward an actor supplied by a route, header, query, body, or
module field.

The ownership line is strict:

- the module names what happened: subject kind, subject id, action, and small
  structured details;
- API Platform proves the app, module, actor, request, and connection context
  from the original signed invocation.

That keeps caller-authored data useful without letting it become identity
evidence.

## Record inside the mutation transaction

Pass the same querier used by the mutation:

```go
import "github.com/mirrorstack-ai/app-module-sdk/audit"

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

`audit.Record` inserts into an SDK-owned table in the current app schema. A
commit preserves both the change and its audit row; a rollback preserves
neither. The lifecycle provisioner creates and upgrades this table, so a module
must not add an audit migration.

`Entry` deliberately has no actor, app, module, request, invocation, or
provenance field. Do not hide any of those in `Details`; details are descriptive
business context, not trusted identity.

## Typed invocation is required

At request authentication time the SDK privately retains the exact canonical
invocation bytes accepted from API Platform. `audit.Record` copies those bytes
into the outbox. Module code can read the typed projection through
`invocation.FromContext`, but cannot set or retrieve the private delivery proof.

If the context did not arrive through an authenticated typed transport,
`audit.Record` returns `audit.ErrProvenanceUnavailable` before issuing SQL. This
is expected in a proofless standalone test or a legacy caller. A test for an
audited mutation should exercise the SDK request adapter; it must not invent a
trusted context.

Propagate this error from the transaction. Committing the business mutation
without the matching record would create a gap in a trail that claims to be
complete.

## Delivery and recovery

After a successful `ms.Tx` that called `audit.Record`, the SDK makes one bounded,
best-effort drain attempt. Transactions that did not record an audit event do no
extra query. The mutation does not become failed after commit merely because API
Platform is temporarily unavailable: its outbox row remains durable and retryable.

For a backlog worker or scheduled maintenance handler, call:

```go
err := ms.DrainAudit(ctx)
```

It claims at most one bounded batch. Claims use `SKIP LOCKED`, an expiring
lease, and a rotating fence token, so multiple workers can call it safely. A
stable event id makes an exact replay idempotent at API Platform; reusing that
id for changed content is a conflict.

Modules do not own delivery SQL, HTTP headers, endpoint paths, backoff, or
credential refresh. `ms.DrainAudit` gets renewable module authority from the
current execution context and restores the stored invocation proof only at the
platform ingress boundary.

## Failure behavior

| Failure | Result |
|---|---|
| Missing fields, oversized/unserializable details, or missing typed proof | `audit.Record` fails inside the mutation transaction. |
| Outbox insert fails | The mutation transaction fails. |
| Transaction rolls back | No audit row exists and no drain runs. |
| Network, authority, rate-limit, or temporary platform failure | The committed row is released with bounded backoff. |
| Permanently invalid event (`400`, `409`, `413`, `422`) | The row is quarantined for inspection, never deleted. |
| Retry budget is exhausted | The row is quarantined with its bounded failure code. |
| An older SDK row has no invocation proof | Upgrade quarantines it as `missing_invocation_proof`; the SDK never backfills invented provenance. |

Delivered and quarantined rows remain as local delivery evidence. Neither is
claimable again.

## Security rules

- Never decode or log `X-MS-Invocation`, `Audit.Provenance`, the stored proof,
  the service credential, or an audit-ingress response body.
- Never accept an actor or trusted scope through `audit.Entry` or `Details`.
- Use the original request context in `audit.Record`; do not replace it with
  `context.Background()`.
- Keep `Details` small and structured. Do not store credentials, session tokens,
  raw request bodies, or unnecessary personal data.
- Use `audit.Record` for repeating business facts instead of a module-specific
  ledger. A state column needed by the domain may remain on the domain row, but
  it is not a replacement for the platform audit trail.

## Final-shape checklist

- [ ] Mutation and `audit.Record` use the same `ms.Tx` querier
- [ ] `Entry` contains only subject kind/id, past-tense action, bounded details
- [ ] No custom audit migration, actor field, outbox drainer, or HTTP transport
- [ ] `audit.ErrProvenanceUnavailable` is propagated, not swallowed
- [ ] Backlog execution calls `ms.DrainAudit` rather than copying SDK mechanics
- [ ] Unit tests enter through an authenticated SDK transport when provenance is required

See also [trusted invocation context](invocation.md), [scopes](scopes.md), and
[module structure](module-structure.md).
