package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/ids"
	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
)

// THE CO-LOCATED-PRODUCER BRANCH OF THE DEV PLANE.
//
// This is the dev-plane counterpart to resultDeployed and has deliberately the
// SAME shape: resolve the producer, resolve the physical relation, compose one
// DynamicSelect, run it in a READ ONLY tx, map SQLSTATEs to sentinels. Only the
// MANIFEST CARRIER differs — the deployed plane reads the manifest the platform
// injected down the Lambda envelope, this one reads the dev dependency
// directory (dev_directory.go) that co-located producers self-publish. The
// amended header on dependency_db.go explains why the dev plane needed a second
// sub-path at all: its old premise, "the dev plane holds no socket to the
// platform DB", is true of a REMOTE producer and false of a co-located one.
//
// WHAT THIS REPRODUCES OF PRODUCTION'S AUTHORIZATION WALK. The platform's
// PgCatalog.AuthorizeExposedRead runs five steps on a READ ONLY tenant tx.
// authorizeLocalRead below reproduces four of them, in a weaker but honest
// form, and CANNOT reproduce the fifth (app-owner consent) because dev has no
// install lifecycle to consent within. See authorizeLocalRead for the step-by-
// step mapping and KNOWN LIMITS below for what that costs.
//
// KNOWN LIMITS (this is an honest dev-mode fail-fast, not a security boundary —
// production enforcement is the DB role's grants):
//
//  1. APP-OWNER CONSENT IS UNREPRESENTABLE — the headline gap. Prod step 5
//     requires a cross_module_consents row a human app owner approved at
//     install time. The local database has no installs, no consents and no
//     exposure tables at all. So LOCALLY, declaring a dependency is SUFFICIENT
//     to read it; in PROD, declaring is necessary but never sufficient. A
//     module can work perfectly all the way through dev and be refused on
//     install. That is a real cliff, and it is inherent to dev having no
//     approval surface — not something this file could paper over.
//     What this file DOES owe the author is a warning, because internal/ is a
//     package no module author reads: resultLocal logs the gap once per
//     producer on the first co-located resolution, and the public
//     ms.DependencyDB doc comment (mirrorstack.go) states it next to
//     ErrNotExposed. Those two are the only surfaces an author actually sees.
//  2. NO VERSION ANCHORING. Prod anchors exposure to the version the producer
//     provably runs in that app (install pin, else serving deploy, never
//     latest-published). Dev has neither — the running source IS the version.
//     Adding an ms.ExposeTable grants a local read on the next boot; in prod
//     it takes effect only after publish AND the app moving to that version.
//  3. NO DB-LEVEL CEILING. The deployed plane's whole safety argument is that
//     Postgres enforces the install-time GRANT, so a wrong or forged relation
//     name cannot over-read. Verified void locally: the dev pool connects as
//     `mirrorstack` with rolsuper = t, and pg_roles holds zero r_<app8>_<mod>
//     roles. The Go walk below is the ONLY authorization; a bug in it is a full
//     read of any module's tables with no second line of defense.
//  4. BOTH ASSERTIONS ARE SELF-ATTESTED OVER A SHARED SUPERUSER CONNECTION. A
//     malicious module can UPDATE another module's directory row, or skip the
//     SDK entirely with db.Open(). What this buys is that an HONEST module
//     cannot accidentally read what prod would refuse.
//  5. CROSS-APP ISOLATION RESTS ON ONE LINE. Three app schemas share one
//     database under one role. `Schema: db.SchemaFrom(ctx)` in readGrantedRows
//     is the entire defense — no per-app role, no RLS (pg_policies returns 0
//     rows), no app_id column. Any refactor that sources the schema from
//     anywhere caller-derived is a tenant-data leak that prod makes
//     structurally impossible.
//  6. A STRICTLY STRONGER SOURCE EXISTS AND IS DELIBERATELY DEFERRED. The CLI
//     sees both sides at tunnel-up and already fetches each module's manifest
//     for `module record`. It could compute, PER CONSUMER, the intersection of
//     that consumer's DependsOn tables with the producer's exposes.tables and
//     hand each module only its own entitlements — the local analogue of prod's
//     per-role GRANT, immune to a sibling forging a row. That roster is
//     literally a []db.DependencyGrant, so it drops in at higher precedence in
//     resultLocal step 2 with no change to readGrantedRows, the plane switch,
//     or any sentinel. Not v1 only because it needs a coordinated CLI release.

// resultLocal is the co-located-producer branch of the dev plane.
//
// THE FALLTHROUGH RULE, stated once: handled=false — meaning "fall through to
// the read-exposed proxy" — is returned IF AND ONLY IF this session could not
// establish that the producer is co-located. It is NEVER returned because
// authorization failed and NEVER because a read failed. Once a directory entry
// is in hand the local plane OWNS the request: retrying a co-located read
// against prod dispatch would join PROD rows against LOCAL rows, which is wrong
// data — strictly worse than an error, and half the reason this branch exists.
// localDevBypassAppID mirrors the synthetic AppID that auth.PlatformAuth's
// Step-2 local-dev bypass injects when no platform secret is configured
// (auth/middleware.go). Duplicated as a constant rather than imported because
// internal/core cannot depend on auth without an import cycle; it is used only
// to make an error message name the real cause, never to grant anything, so a
// drift in the literal degrades the message and nothing else.
const localDevBypassAppID = "local-dev-app"

// pgerrcodeUndefinedTable is Postgres 42P01. Named because the local plane
// treats it as a distinct, actionable condition rather than a generic read
// failure — see the read-error handling in resultLocal.
const pgerrcodeUndefinedTable = "42P01"

func (q *DependencyQuery) resultLocal(ctx context.Context) (*DependencyResult, bool, error) {
	// Plane gate. m.devMode is the SDK's canonical "running under the
	// `mirrorstack dev` lifecycle" signal — captured once at New() from
	// devMigrateEnabled(), which is MS_LOCAL_DB_URL AND not Lambda AND not
	// task-worker (dev_migrate.go:32). It is also the exact flag that attaches
	// devAppSchemaMiddleware (module.go), so gating on it makes "the local DB is
	// reachable" and "the app schema is in ctx" ONE condition instead of two.
	//
	// Reading the env var directly here instead — as this originally did — is
	// STRICTLY WEAKER than the sentence above, and the gap is not theoretical:
	// a task worker and a Lambda both see MS_LOCAL_DB_URL in a dev session but
	// have no app-schema middleware attached, so the local branch could fire in
	// a process where nothing ever put a schema in ctx. Gate on the flag whose
	// meaning the comment claims.
	if !q.dep.mod.devMode {
		return nil, false, nil
	}

	// A boot-time publish failure leaves THIS module invisible to co-located
	// consumers. One-shot retry, on the only in-process event guaranteed to
	// happen after boot. Deliberately before the lookup and deliberately not
	// error-checked: it heals this module's own visibility, and has nothing to
	// say about whether the producer being read is co-located.
	q.dep.mod.ensureDevDirectoryPublished(ctx)

	entry, ok, err := q.dep.mod.colocatedProducer(ctx, q.dep.producer)
	if err != nil {
		// A broken local OPTIMIZATION must not break a working remote read.
		// This cannot leak empty rows: the proxy path it falls back to is
		// itself fail-closed on every status it does not recognize.
		q.dep.mod.logger.Printf("dev: dependency directory lookup for %q failed, using the read-exposed proxy: %v", q.dep.producer, err)
		return nil, false, nil
	}
	if !ok {
		// A producer that runs only in prod is a LEGITIMATE cross-plane read,
		// and so is one whose directory lease has EXPIRED (it stopped running).
		// Turning either into ErrProducerNotFound would break every
		// genuinely-remote producer — local-first must be strictly ADDITIVE.
		return nil, false, nil
	}

	// From here on every return has handled = true.

	if !authorizeLocalRead(q.dep.mod.registry.Dependencies(), entry, q.table) {
		return nil, true, fmt.Errorf("%w: %q is not exposed to this module by %q in this dev session", ErrNotExposed, q.table, q.dep.producer)
	}

	// The directory row arrived from ANOTHER process over a shared table, so it
	// is untrusted input no matter how honest its author. moduleIDPattern
	// (module.go:196) is the first of three gates; selectIdentPattern inside
	// buildDynamicSelect is the second, pgx.Identifier.Sanitize() the third.
	// Plain error, no sentinel: a malformed directory row is a broken local
	// session, not an authorization verdict about this consumer.
	if !moduleIDPattern.MatchString(entry.ModuleID) {
		return nil, true, fmt.Errorf("mirrorstack: DependencyDB: dependency directory row for %q carries an invalid module id %q", q.dep.producer, entry.ModuleID)
	}

	// buildDynamicSelect hard-fails on zero columns (select.go:120) — the
	// blessed composer never emits SELECT *. The proxy supports an empty
	// projection only because it expands it via a visibleColumns
	// information_schema probe run THROUGH THE RESTRICTED CONSUMER ROLE, which
	// is what makes that probe an authorization signal at all. Locally
	// resolvePoolFor (db.go:103) hands back the shared SUPERUSER pool, so the
	// identical probe would see every table and carry no authorization meaning
	// whatsoever — it would silently WIDEN the surface and hand back columns
	// that have no GRANT in prod. Falling through to the proxy is equally wrong
	// (prod rows joined against local rows). So: fail loud.
	if len(q.columns) == 0 {
		return nil, true, errors.New("mirrorstack: DependencyDB: a co-located dev read requires an explicit projection — call Columns(...); the empty-projection form is resolved by the read-exposed proxy and has no local equivalent")
	}

	// THE SAFETY-CRITICAL LINE. app_a722a8a8_d413_435b_b21b_f4cbacb5ef73,
	// app_twkpa_edu and app_dev share ONE database under ONE superuser, so this
	// schema binding is the ENTIRE tenant-isolation story locally. It must come
	// from db.SchemaFrom(ctx) — injected by devAppSchemaMiddleware
	// (module.go:168) from the trusted X-MS-App-ID header — and from nothing
	// caller-derived, ever. Prod makes a cross-app read structurally impossible
	// via per-app schema plus per-(app,module) role; here only this does.
	// Checking explicitly also avoids requireSafeSelectIdentifiers failing
	// opaquely with errUnsafeSelectIdentifier on an empty Schema
	// (select.go:171).
	schema := db.SchemaFrom(ctx)
	if schema == "" {
		return nil, true, errors.New("mirrorstack: DependencyDB: this request carries no app scope (no X-MS-App-ID header), so there is no app schema to read the producer's table from")
	}

	// AND PIN IT TO THE TRUSTED APP SCOPE. db.SchemaFrom(ctx) alone is one
	// derivation of the app scope; q.dep.appID (appIDFromContext → auth.Get) is
	// the OTHER, and it is the one the proxy path has always used to address the
	// read. Checking only the first leaves the schema binding as good as
	// whatever most recently called db.WithSchema on this context — which in a
	// process with no app-schema middleware, or one where a seam later sets the
	// schema from somewhere else, is not necessarily this app. With three app
	// schemas in one database under one superuser and no RLS, a divergence there
	// is a cross-tenant read with nothing underneath it to stop the query.
	//
	// Both sides derive through runtime.AppSchemaName (devAppSchemaName here,
	// devAppSchemaMiddleware there), so a legitimate dev request can never
	// false-positive: same input, same function, same output. A mismatch means
	// the two scopes genuinely disagree, and the only safe reading of that is to
	// refuse. Plain error, no sentinel — a disagreeing context is a broken
	// session, not an authorization verdict about this consumer.
	want, ok := devAppSchemaName(q.dep.appID)
	if !ok || want != schema {
		// Name the synthetic bypass identity explicitly. It is by far the
		// likeliest way to reach this line — running plain `mirrorstack dev`
		// configures no platform secret, so auth.PlatformAuth's Step-2 bypass
		// mints localDevBypassAppID while the schema is still derived from the
		// real X-MS-App-ID header — and reading a bare cross-tenant refusal when
		// the true cause is "no --tunnel" sends a developer hunting a security
		// bug that is not there. The verdict is unchanged either way.
		if q.dep.appID == localDevBypassAppID {
			return nil, true, fmt.Errorf("mirrorstack: DependencyDB: a co-located read needs `mirrorstack dev --tunnel` — with no platform secret configured the local-dev bypass mints app %q, which cannot match this request's app schema %q", q.dep.appID, schema)
		}
		return nil, true, fmt.Errorf("mirrorstack: DependencyDB: refusing a co-located read whose app scope does not match its schema binding (app %q derives schema %q, but this request is bound to %q)", q.dep.appID, want, schema)
	}

	physical, err := localPhysicalName(entry.ModuleID, q.table)
	if err != nil {
		return nil, true, err
	}

	// TELL THE DEVELOPER WHICH PLANE RAN, AND WHAT IT DOES NOT ENFORCE.
	//
	// Two failures of this feature are silent without this line. The first is
	// mundane: a co-located read and a proxied read are indistinguishable from
	// the outside, so "why am I seeing stale rows" has no observable answer.
	// The second is the one that costs a release. KNOWN LIMIT 1 above is a
	// PARITY GAP, not a footnote: locally, DECLARING a dependency authorizes the
	// read; in production the app owner must ALSO have consented at install
	// time. An author can therefore build a whole consumer under dev where every
	// cross-module read succeeds and first learn otherwise from a production
	// ErrNotExposed. Nothing else in the system says so at the moment it
	// matters — internal/ is invisible to module authors, and by the time the
	// install is refused the code is written.
	//
	// Once per PRODUCER, not once per read: a consumer polling a producer would
	// otherwise emit this on every request and train the developer to filter it.
	if _, seen := q.dep.mod.devDir.announced.LoadOrStore(entry.ModuleID, struct{}{}); !seen {
		q.dep.mod.logger.Printf("dev: reading %s.%s from co-located producer %s (%s) — app-owner consent is NOT enforced in dev; this read can still be refused at install time in production",
			q.dep.producer, q.table, entry.Slug, entry.ModuleID)
	}

	// The local path loses the implicit 15s bound the proxy had via callHTTP
	// (call.go:29), so re-establish it with the SAME callTimeout constant —
	// both dev transports then carry an identical wall-clock bound and no new
	// number enters the codebase. Deliberately NOT a SET LOCAL
	// statement_timeout: there are zero occurrences of that anywhere in db/ or
	// internal/core/, and bounding is Go-side by convention (call.go:29,
	// task.go:118).
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	rows, truncated, err := q.readGrantedRows(ctx, physical)
	if err != nil {
		// %w preserves errors.Is, so mapDeployedReadError's
		// ErrDependencyUnavailable still matches while the message gains the
		// schema and relation. That matters because 42P01 is the case that will
		// actually bite: dev app schemas are provisioned lazily and
		// INDEPENDENTLY per module (ensureDevAppSchema, dev_migrate.go), and
		// provisioning is asymmetric in practice — app_dev holds only
		// users-profile's tables, so a read scoped there hits a relation that
		// does not exist even though oauth-core is running fine and has simply
		// never been touched for that app. Correctly fail-closed, but the
		// developer needs to see WHICH schema to make sense of it.
		// For 42P01 specifically, say what to DO about it. The sentinel this
		// wraps is documented as "producer yanked/rolled back, grant revoked",
		// and consumers surface it as a 503 — so the default text accuses a
		// producer that is running perfectly, and the developer has no path from
		// the message to the one-line fix.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgerrcodeUndefinedTable {
			return nil, true, fmt.Errorf("%w (local read of %s.%s): the producer is co-located but has not provisioned this app's schema yet — dev app schemas are created lazily on each module's FIRST request for an app, so send one request to %s for this app and retry", err, schema, physical, entry.Slug)
		}
		return nil, true, fmt.Errorf("%w (local read of %s.%s)", err, schema, physical)
	}

	rows, err = normalizeLocalRows(rows)
	if err != nil {
		return nil, true, fmt.Errorf("mirrorstack: DependencyDB: normalize local rows: %w", err)
	}
	return &DependencyResult{Rows: rows, Truncated: truncated}, true, nil
}

// authorizeLocalRead is the local analogue of the platform's
// PgCatalog.AuthorizeExposedRead walk. Both halves are checked before any
// relation name is composed and long before a pool is acquired.
//
// Prod runs five steps on a READ ONLY tenant tx; this reproduces four:
//
//  1. producer resolves in this app's install set -> the directory hit that got
//     us here (dev has no install set; co-location is the analogue).
//  2. consumer install row exists -> the consumer's own compiled ms.DependsOn
//     declaration, read here from its registry.
//     3.+4. table is exposed by the version the producer PROVABLY runs ->
//     entry.Exposes, published by the producer's own process from its own
//     registry.ExposedTables(). In dev the running source IS the running
//     version, so there is nothing to anchor to. Prod additionally requires the
//     consumer to have DECLARED the table (module_version_dependency_tables,
//     folded into the GRANT) — that is the Tables check below.
//  5. app-owner consent (cross_module_consents) -> NOT ENFORCEABLE. Dev has no
//     install lifecycle and no consent table at all. See KNOWN LIMITS above.
//
// The producer ref may be a slug, an m<hex> id or a dashed UUID, and the
// consumer's declaration may spell it any of those ways, so a declaration
// matches on EITHER of the directory entry's two identities — through
// directoryLookupKeys, which is the SOLE OWNER of the ref-equivalence rule.
//
// Routing the declaration through the same function the DIRECTORY LOOKUP uses
// is the whole point, not a convenience. When the two sides spelled the rule
// separately they drifted, and the drift was a live false denial: a dependency
// declared as a dashed UUID — a form registry.ValidateDepID accepts and
// DependencyDB documents — normalized to m<hex> for the lookup (so the
// directory HIT, handled=true, no fallthrough) and was then compared RAW here,
// failing to match entry.ModuleID and returning ErrNotExposed for a table that
// was both declared and exposed. One rule, one function, and the two sides
// cannot disagree again.
//
// parseProducerRef is called on d.ID for shape rejection, not for its stripping
// behavior: registry.ValidateDepID's depIDPattern already forbids both an owner
// suffix and a second '@', so the version-stripping branch is DEFENSIVE here —
// unreachable from an ID the registry actually stored. It stays because
// authorizeLocalRead takes a []registry.Dependency, not a registry, and a
// caller that hand-builds one should not get a different answer.
func authorizeLocalRead(deps []registry.Dependency, entry devModuleEntry, table string) bool {
	for _, d := range deps {
		ref, err := parseProducerRef(d.ID)
		if err != nil {
			continue // an unparseable declaration cannot authorize anything
		}
		keys := directoryLookupKeys(ref)
		if !slices.Contains(keys, entry.Slug) && !slices.Contains(keys, entry.ModuleID) {
			continue
		}
		if !slices.Contains(d.Tables, table) {
			continue // declared the producer but not this table
		}
		return slices.Contains(entry.Exposes, table)
	}
	// A consumer-side miss and a producer-side miss are DELIBERATELY
	// indistinguishable — one bool here, one message at the call site. This
	// preserves the anti-probing collapse documented at dependency_db.go:78 and
	// mirrored by both existing planes (resultDeployed's :467 and the proxy's
	// collapsed 403). A consumer must not be able to enumerate a sibling's
	// exposure set by error shape.
	return false
}

// localPhysicalName composes the physical relation name for a co-located
// producer's exposed table.
//
// This is the SDK's FIRST-EVER derivation of a physical name and a deliberate,
// scoped departure from db/dependency.go's contract ("The SDK never derives the
// physical name; it reads it here"). That contract holds because the platform
// is there to compute it via ids.PhysicalTableName; a co-located dev session
// has no platform, so the SDK reconstructs the identical rule. It lives in
// exactly one function so there is exactly one place the rule exists.
//
// producerID ALREADY carries its leading "m" (Config.ID is m<32 hex>), so the
// rule is <producerID>_<table>, NOT "m"+producerID+"_"+table. The migration
// source is the proof: oauth-core/sql/app/0001_init.up.sql declares
// `CREATE TABLE IF NOT EXISTS __MODULE_ID___users` — three underscores, i.e.
// token + "_" + "users" — resolved by placeholderQuerier.resolve
// (db_placeholder.go:69) with p.id == Config.ID. A second "m" yields a name
// that 42P01s.
//
// The shape check here rather than in buildDynamicSelect turns an opaque
// errUnsafeSelectIdentifier into a message that names the actual budget.
func localPhysicalName(producerID, table string) (string, error) {
	name := producerID + "_" + table
	if !selectIdentPattern.MatchString(name) {
		return "", fmt.Errorf("mirrorstack: DependencyDB: physical relation name %q for %q.%q is not a valid Postgres identifier (33-byte module id + 1 + table name must stay within 63 bytes)", name, producerID, table)
	}
	return name, nil
}

// normalizeLocalRows converts pgx's native row values into the JSON-shaped
// values a dev-plane caller already receives, so switching a co-located read
// off the proxy cannot change what the consumer sees.
//
// The proxy returns JSON, decoded with dec.UseNumber() (dependency_db.go:420):
// uuid arrives as a string (the platform's normalizeWireRows rewrites the
// binary form), timestamptz as RFC3339, every number as json.Number.
// pgx.CollectRows(pgx.RowToMap) (select.go:98) returns NATIVE Go types.
// Measured against the live dev Postgres:
//
//	uuid        -> [16]uint8        fmt.Sprint "[202 60 140 239 ...]"
//	timestamptz -> time.Time
//	bigint      -> int64
//	numeric     -> pgtype.Numeric   fmt.Sprint "{15 -1 false finite true}"
//
// oauth-core's users.id IS uuid, and the consumer's stringField falls through
// to fmt.Sprint — so a raw local path silently corrupts the very join key this
// whole surface exists to carry.
//
// The fix REPRODUCES the proxy's pipeline rather than enumerating types: a
// [16]byte pre-pass, then json.Marshal + a UseNumber decode. Two reasons the
// pre-pass cannot be skipped and the marshal cannot be replaced by a type
// switch: json.Marshal renders [16]byte as the ARRAY [202,60,...], not a uuid
// string; and every pgtype (Numeric today, whatever lands tomorrow) already
// implements json.Marshaler, so the round trip is correct for types no switch
// would know about. Cost is one marshal over at most 2000 dev rows.
//
// The pre-pass RECURSES, and has to. pgx decodes a `uuid[]` column into a
// []any of [16]byte, and a composite or jsonb value into a map[string]any that
// can hold one at any depth — so a top-level-only walk leaves exactly the same
// corrupted join key this function exists to prevent, just one level down and
// harder to spot. Recursion is over []any and map[string]any only, which is the
// complete set of container shapes pgx.RowToMap produces; anything else is a
// leaf and is handed to json.Marshal as-is.
//
// Applied on the LOCAL branch ONLY. resultDeployed has the same latent
// divergence — its integration test dodges it with `id bigint` — but fixing
// that changes deployed-plane output and belongs in its own PR.
func normalizeLocalRows(rows []map[string]any) ([]map[string]any, error) {
	for _, row := range rows {
		normalizeUUIDs(row)
	}
	buf, err := json.Marshal(rows)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(buf))
	dec.UseNumber()
	var out []map[string]any
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	if out == nil {
		// A nil input marshals to `null` and decodes straight back to nil, so
		// this guard is load-bearing rather than defensive: Rows is never nil
		// on success (dependency_db.go:429).
		out = []map[string]any{}
	}
	return out, nil
}

// normalizeUUIDs rewrites every [16]byte in a decoded row — at any depth — to
// its canonical uuid text form, mutating in place.
//
// Mutation rather than a rebuilt copy is deliberate: the maps and slices come
// straight from pgx.CollectRows and belong to this call, nothing else holds a
// reference, and rebuilding them would allocate a second full copy of the
// result set for no observable difference.
//
// Only []any and map[string]any are descended into. That is not a guess about
// the shape of the data — it is the complete set of containers pgx.RowToMap can
// hand back, since anything structured arrives already decoded into one of the
// two. A typed slice ([]string, []int64) cannot contain a [16]byte by
// construction, so skipping it costs nothing.
func normalizeUUIDs(v any) any {
	switch t := v.(type) {
	case [16]byte:
		return ids.FormatUUID(t)
	case map[string]any:
		for k, elem := range t {
			t[k] = normalizeUUIDs(elem)
		}
	case []any:
		for i, elem := range t {
			t[i] = normalizeUUIDs(elem)
		}
	}
	return v
}
