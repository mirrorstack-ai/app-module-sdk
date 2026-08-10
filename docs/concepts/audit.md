# Audit trails

A module that changes who can do what owes an answer to "who did this, and how".
This page is the rule for producing that answer, and the reasoning behind each
part of it. Two modules supply the examples — users-roles records role grants
and revocations, oauth-core records session revocations — and they landed on
different shapes for good reasons, which is where this starts.

The failure this exists to prevent is not a missing feature. It is a trail that
is **believed** and wrong: incomplete, invented, or attributable to whoever the
caller claimed to be.

## 1. Pick the shape before writing anything

| The event | Shape | Why |
| --- | --- | --- |
| Repeats for the same subject | Append-only ledger table | Each occurrence is a fact; the previous one must survive |
| Terminal — happens once, ever | Provenance columns on the row | There is no history to accumulate |

Role grants repeat: a user can be granted `member`, lose it, and be granted it
again, and all three are separate facts. That needs a ledger.

Session revocation is terminal: a session is revoked once and stays revoked, so
`revoked_by` / `revoked_via` on the session row carries everything. A ledger
would be a table with one row per subject forever.

Getting this backwards is expensive in both directions. A ledger for a terminal
event is dead weight; columns for a repeating event silently overwrite history,
which is the worst outcome available — it looks like an audit trail and destroys
evidence.

**Watch for a `DELETE` that erases the record.** users-roles kept `granted_by`
on the current row, but revoking was a plain `DELETE`, so the trail was being
destroyed by design and nobody noticed until someone asked to see it. If your
"current state" columns are the only provenance you have, a delete is a
shredder.

## 2. Provenance is two fields, never one

Record an **actor** (who) and a **via** (by what mechanism). Both, always.

A single nullable actor cannot distinguish these three cases, and they are not
the same thing:

- the platform did it automatically, so there is no human actor;
- a human did it and we could not identify them;
- we forgot to record it.

`via` resolves all three. `via=session_limit` with a NULL actor is an automatic
prune — no human was involved, by definition. `via=operator` with a NULL actor
says a person did this and their identity was unavailable. A NULL actor with no
`via` says nothing at all, and will be read as whichever of the three the reader
already believes.

Constrain `via` with a CHECK. It is a closed vocabulary, and an unconstrained
text column becomes a free-text field that nobody can query.

## 3. The actor comes from the request context. Only.

Never a header, never a query parameter, never a body field. An audit record the
caller can write is worse than no record, because it will be trusted.

Take it from the SDK-promoted request identity and nothing else.

**Better still, derive it from stored state where the semantics allow.** A
user logging themselves out is `actor = the session row's own user_id` — read
from the row being modified, not from the request. That is unforgeable *by
construction* rather than by validation, and construction does not have bugs.

**Identity is not authorization, and this matters here specifically.** By the
time an audit write happens, authorization has already been settled by the
permission gate. An unidentifiable actor is not an unauthorized one, so failing
the operation because provenance is unavailable takes a control away from
someone who legitimately holds it. Record what you know, say what you do not.

**Validate against the column type before writing.** `revoked_by` is a `uuid`,
and the SDK's local-dev bypass injects a synthetic non-UUID identity — writing
it unchecked failed with `22P02`, which left the session live while the route
reported 500 and rolled an enclosing transaction back. So:

1. no identity → NULL actor, real `via`;
2. identity present but unusable for the column → NULL actor, real `via`, and log it;
3. identity usable → record it.

Never (4): refuse the operation.

## 4. Coverage must be structural, not remembered

An enumerated list of call sites is a list that goes stale. users-roles has
**eleven** call sites mutating one table across five logical paths, one of them
outside `internal/` entirely — the kind that is missed.

So put the audit write **in the statement that does the mutation**: a
data-modifying CTE whose `RETURNING` feeds the audit `INSERT`. A new writer then
cannot forget, because there is nowhere to forget it from. Call sites that were
never edited stay covered.

This also gives correct behaviour for free. Grants that are
`ON CONFLICT DO NOTHING` return no rows when the state was already correct, so a
re-grant appends **no** audit row. A ledger full of events that did not happen is
as misleading as one missing events, and hand-written audit calls at call sites
get this wrong by default — they fire on intent, not on change.

Back it with a test that reads the SQL, requires an audit append beside every
mutation of the table, **and pins the count**. Without the count, a new writer
lands silently.

## 5. Never imply completeness you do not have

A bounded read must say it is bounded. Showing the newest N without saying so
presents a partial trail as the whole history — the one way an audit surface
actively deceives.

Fetch `N + 1` rows, return `N`, and let the extra row set a `hasMore` flag. It
costs one row instead of a second `COUNT(*)` over a table that only grows. Send
the flag onward rather than letting a consumer infer truncation from the row
count: only the producer knows its own page size.

## 6. Retention rules

- **Append only.** No `UPDATE`, no `DELETE`. Assert it in a test.
- **No foreign key to the thing being described.** Keep `role_key` as text with
  no FK to `roles`, so deleting a role does not erase the history of who held it.
  History must outlive its subject; that is the point.
- **Never backfill.** Rows predating the provenance columns have no actor, and
  they must read as *unknown*. Inventing a plausible actor for historical rows is
  a lie in the one place lies are most costly. "We started recording this on
  date X" is a fine thing for a UI to say.

## 7. Do not gate the read on an actorless transport

Do not put `RequirePermission` on an `Internal` read. The service call carries
the app id and credentials, but no caller: `ms.Call` sends no actor, and dispatch
supplies the target module's internal secret. Without a fabricated role,
`RequirePermission` returns `401` for that service identity.

That failure is worse than a denied page. A host fetching a contributed audit
list degrades a failed fetch to "no audit section", so the `401` renders as
*"this subject has no history"* — a security ledger disappearing in silence.
Re-adding the permission gate does not restore a caller boundary; it breaks the
actorless read.

Because the route cannot authenticate its caller, browser-originated `Internal`
paths must be refused by the platform. Today the platform does not refuse them
for dev-mounted modules. The dispatch mount is unauthenticated at the HTTP edge,
and its only path-shape rejection covers the `__mirrorstack` namespace, so
`/internal/...` is forwarded to the module. The handler injects the module
owner's user id and a hardcoded `admin` app-role together with the session's
internal secret, handing the module a privileged identity no caller presented.

The five-condition dev gate — app dev-mode, module dev-mode, `dev_mount`, an
install row, and a live tunnel — controls whether the localhost plane is open.
It does not establish who is asking. On the dev plane, an `Internal` route is
not a confidentiality boundary. Do not place behind it anything that would be
harmful to expose to whoever can reach the dev edge.

Production modules are not dev-mounted, so the dev-tunnel branch described here
does not apply there. That scopes this gap; it does not make `Internal` a caller
boundary.

## 8. Checklist

- [ ] Ledger for repeating events, provenance columns for terminal ones
- [ ] Both `actor` and `via`; `via` CHECK-constrained
- [ ] Actor from request context only — derived from stored state where possible
- [ ] Actor validated against the column type; unusable → NULL, never a refusal
- [ ] Audit write inside the mutating statement, not at call sites
- [ ] No audit row when nothing actually changed
- [ ] Test parses the SQL, requires an append per mutation, pins the count
- [ ] Append-only asserted; no FK to the described entity
- [ ] Bounded reads report `hasMore`; the surface says it is capped
- [ ] No backfill; pre-existing rows read as unknown
- [ ] `Internal` read not permission-gated; its data is safe to expose to anyone
  who can reach the dev edge
- [ ] Migration embedded through `ms.Config.SQL`; manifest `migration` reflects
  it

See also [scopes](scopes.md), [internal-calls](internal-calls.md),
[manifest](manifest.md).
