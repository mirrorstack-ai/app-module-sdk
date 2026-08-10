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
on the assignment row while revoking was a plain `DELETE` — so the revoke
destroyed the only provenance the module had. If your "current state" columns
are the only provenance you have, a delete is a shredder.

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
and the SDK's local-dev bypass mints the synthetic identity `local-dev-user`,
which is not one. Writing it unchecked failed at the database with `22P02`: the
session stayed live and the route answered 500, and on the path where the same
write ran inside `ms.Tx` it took the enclosing transaction down with it. So:

1. no identity → NULL actor, real `via`;
2. identity present but unusable for the column → NULL actor, real `via`, and log it;
3. identity usable → record it.

Never (4): refuse the operation.

## 4. Coverage must be structural, not remembered

An enumerated list of call sites is a list that goes stale. users-roles has
**eleven** call sites mutating `user_roles`, spread over **four files**:
`internal/handlers/routes.go` (4),
`internal/handlers/approval_transition.go` (3),
`internal/handlers/self_service.go` (3), and `declare/default_role.go` (1) —
the last one outside `internal/` entirely.

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

## 7. Keep Internal reads honest

**Do not put `RequirePermission` on an `Internal` read.**
`Module.RequirePermission(name)` returns `auth.RequireRoles(admin, ...declared)`:
it decides from the app role on the request identity, and admin passes whatever
the permission declares. `auth.RequireRoles` answers 401 `authentication
required` when there is no identity or its `AppRole` is empty, and 403
`forbidden` on a role miss. But an Internal hop is actorless: `ms.Call` carries
a verified actor only to a canonical `/platform` path, so a Public or Internal
call stays a service call even when it originates inside a Platform handler. The
gate is therefore not evaluating your caller. It is deciding about the absence
of one, on whatever identity the platform edge did or did not attach — which
makes it read like a control in the source without being one.

**Do not take the edge's behaviour from prose, including this page's.** Two
characterization suites exist precisely because design documents about this kept
asserting the opposite of the code, and they are the executable answer. In this
repo, [`auth_scope_truth_test.go`](../../auth_scope_truth_test.go) —
`TestCharacterization_RequirePermissionAdmitsAdminRegardlessOfDeclaredRoles` and
`TestCharacterization_RoleGateReturns401WithoutActor_403OnRoleMiss` pin the gate
described above. In api-platform,
`internal/dispatch/handler/module_edge_truth_test.go` and
`module_edge_router_wiring_test.go` — `TestModuleEdge_RouteClassTrustMatrix`,
`TestModuleEdge_StampsNoFabricatedRole` and
`TestModuleEdgeIsMountedWithoutAuthMiddleware` pin what the edge hands a module,
per route class. Read them before writing anything about this, and cite them
instead of paraphrasing them. Both files say the same thing about themselves:
changing an assertion there is a security decision, not test fixup.

**Distinguish "empty" from "could not load".** `ms.Call` returns an error for
every non-2xx response. A host that degrades a failed audit fetch into "no audit
section" renders that failure as *"this subject has no history"* — a security
ledger vanishing silently, which is the one thing an audit surface must never
do. Say which of the two happened.

**Scope is not a confidentiality boundary.** `PlatformAuth` and `InternalAuth`
read the same secret source, so one internal secret authenticates both scopes —
`TestCharacterization_InternalSecretSatisfiesPlatformAuth_ScopesAreNotDisjoint`
pins that. And when no secret source is configured at all and the process is not
in Lambda, `PlatformAuth` mints a synthetic `local-dev-user` admin while
`InternalAuth` bypasses its check outright. Design so the answer stops mattering:
put nothing on an `Internal` route that would be harmful to hand to a caller you
did not authorize. Real protection lives in the handler, or in what you store —
never in the scope keyword.

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
- [ ] `Internal` read not permission-gated; its data safe to hand to a caller you
  did not authorize
- [ ] Failed read distinguished from empty read at every layer that renders it
- [ ] Migration embedded through `ms.Config.SQL`; manifest `migration` reflects
  it

See also [scopes](scopes.md) and [manifest](manifest.md). For what the auth
model actually does, read the suites rather than the prose:
[`auth_scope_truth_test.go`](../../auth_scope_truth_test.go) in this repo, and
`internal/dispatch/handler/module_edge_truth_test.go` plus
`internal/dispatch/handler/module_edge_router_wiring_test.go` in api-platform.
