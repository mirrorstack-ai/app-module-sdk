package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/db"
)

// ms.DependencyDB — the RESTRICTED consumer accessor for reading a producer
// module's exposed tables. PLANE-TRANSPARENT: the same call works on both
// planes and the switch lives entirely inside result(ctx, deployed) — where
// `deployed` is the PER-REQUEST deployed-envelope signal (auth.PayloadTrusted),
// NOT the process-global runtime.IsLambda(). The envelope signal is set for
// every deployed invoke on BOTH the real Lambda entrypoint and the local dev
// lambda-invoke shim, so the deployed read fires in the shim too — not only in
// real Lambda (where IsLambda would be true).
//
// A consumer that declared ms.DependsOn("@owner/producer", n.Table("users"))
// issues a STRUCTURED read (table + projection + equality/IN filters, never
// raw SQL), authorized against the same consent+exposure catalog the
// install-time grant walk uses and executed as the consumer's own
// r_<app8>_<mod> role inside a READ ONLY transaction:
//
//   - DEV plane (decision 17 §2, option (d)), TWO sub-paths. The original
//     premise — "the dev plane holds no socket to the platform DB" — is true of
//     a REMOTE producer and FALSE of a co-located one: under `mirrorstack dev`
//     the producer and consumer are two processes sharing one Postgres and one
//     app schema. So:
//     (a) CO-LOCATED producer — resolved through the dev dependency directory
//     (dependency_local.go) — is read LOCALLY, through the same
//     DynamicSelect + db.TxReadOnly + SQLSTATE mapping the deployed plane
//     uses. This is not merely faster: the proxy would return PROD rows to a
//     consumer whose own rows are LOCAL, so routing a co-located read
//     remotely returns WRONG DATA, not just slow data.
//     (b) everything else ships over the platform's read-exposed proxy,
//     authenticated by the live dev-tunnel session secret, exactly as before.
//     Local-first is strictly ADDITIVE: a producer absent from the directory
//     keeps the proxy path byte-for-byte unchanged. On both sub-paths a
//     cross-plane SQL JOIN remains structurally impossible — fetch, then join
//     in app code.
//   - DEPLOYED plane (decision 18 §3): the platform ships the authorized
//     dependency set down the trusted Lambda envelope as a manifest; the SDK
//     composes a sanitized dynamic SELECT against the platform-supplied
//     physical relation name and runs it on mod.DB's already-vended
//     consumer-role pool. Same fetch-then-join floor.
//
// Fetch-then-join-in-app-code is the PERMANENT contract on both planes.
//
//	rows, err := ms.DependencyDB(ctx, "@owner/oauth-core").
//	    Select("users").
//	    Columns("id", "email").
//	    Where("status", "active").
//	    WhereIn("id", 1, 2, 3).
//	    Limit(500).
//	    Rows(ctx)
//
// Every failure is explicit and fail-closed (never silently empty):
// errors.Is against ErrDependencyUnauthorized / ErrNotExposed /
// ErrDependencyUnavailable / ErrProducerNotFound to branch on the
// decision-17 failure modes.
//
// Rollout gate: a deployed consumer running under a platform that does not yet
// inject the dependency manifest keeps failing closed with the "dev-plane only"
// error, so a consumer route stays fail-closed (e.g. 501) until both the
// platform (decision 18 PR 1) and this SDK ship.

// Sentinel errors mapping the read proxy's wire error codes onto the
// decision-17 failure modes. Match with errors.Is; the returned errors wrap
// these plus the platform's human message.
var (
	// ErrDependencyUnauthorized: the platform could not authenticate the
	// read (401) — unknown app, module not resolvable in the app, no live
	// tunnel session, or a wrong/unbound service secret. Deliberately one
	// collapsed verdict; re-establish the tunnel session, don't retry blind.
	ErrDependencyUnauthorized = errors.New("mirrorstack: dependency read unauthorized")
	// ErrNotExposed: authorization failed closed (403 read_not_authorized) —
	// the table is not exposed by the version the producer actually runs,
	// there is no consent row, or the consumer install carries no DB role.
	// The cases are deliberately indistinguishable on the wire.
	ErrNotExposed = errors.New("mirrorstack: table is not exposed to this module")
	// ErrDependencyUnavailable: the read was authorized but the producer's
	// relation is not readable right now (403 dependency_unavailable —
	// producer yanked/rolled back, grant revoked), or the platform's read
	// proxy is not enabled at all. Never surfaced as empty rows.
	ErrDependencyUnavailable = errors.New("mirrorstack: dependency unavailable")
	// ErrProducerNotFound: the producer ref does not resolve to an install
	// in this app (404 producer_not_found).
	ErrProducerNotFound = errors.New("mirrorstack: producer module not found in this app")
)

// Wire caps mirrored from the dispatch read-exposed handler so obviously
// oversized requests fail fast in-process instead of burning a round trip.
const (
	maxDependencyColumns  = 64
	maxDependencyFilters  = 16
	maxDependencyInValues = 200
	maxDependencyBody     = 64 << 10
)

// dependencySQLName validates logical table / column identifiers — the same
// shape the platform enforces (lowercase snake_case, max 63 chars). Logical
// names only: never the physical m<hex>_ relation name.
var dependencySQLName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// Dependency is the read handle DependencyDB returns: the resolved
// (app, consumer, producer) triple every query built from it targets.
// Construction never fails — a bad ref or missing app scope is carried
// forward and surfaced by Rows/Result, keeping call sites chainable.
type Dependency struct {
	mod      *Module // owner module — its Config.ID IS the consumer; deployed branch reads its consumer-role pool
	producer string  // producer ref resolved within the same app (slug | UUID | m<hex>)
	appID    string  // trusted app scope from ctx (auth identity), never caller-supplied
	err      error   // deferred construction error (bad ref, no app scope)
}

// DependencyDB returns a read handle on producerRef's exposed tables within
// the current app. producerRef accepts the same module ref forms as
// ms.DependsOn — "@owner/slug", bare "slug", the m<hex> module ID, or a
// dashed UUID; a trailing @<version-constraint> is tolerated and ignored
// (reads always target the version the producer actually runs, which the
// platform resolves). The app scope is read from ctx via the SDK's auth
// identity — callers don't pass it and cannot forge it.
//
// The returned handle only builds STRUCTURED reads (Select/Columns/Where/
// WhereIn/Limit). It is not a *pgx pool and never will be — see the package
// comment for why raw cross-plane SQL cannot exist.
func (m *Module) DependencyDB(ctx context.Context, producerRef string) *Dependency {
	d := &Dependency{mod: m}
	appID, err := appIDFromContext(ctx, "DependencyDB")
	if err != nil {
		d.err = err
		return d
	}
	d.appID = appID
	producer, err := parseProducerRef(producerRef)
	if err != nil {
		d.err = err
		return d
	}
	d.producer = producer
	return d
}

// DependencyDB returns a read handle on the default module. Panics before
// Init — matching Platform/Public/Internal. See Module.DependencyDB.
func DependencyDB(ctx context.Context, producerRef string) *Dependency {
	return mustDefault("DependencyDB").DependencyDB(ctx, producerRef)
}

// parseProducerRef normalizes a DependsOn-style module ref to the bare form
// the proxy resolves (slug | dashed UUID | m<hex>): strips an optional
// trailing @<constraint> and an optional @owner/ prefix. The proxy resolves
// the producer WITHIN the app, where the bare ref is unambiguous.
func parseProducerRef(spec string) (string, error) {
	ref := strings.TrimSpace(spec)
	// Split on the LAST '@' (same rule as DependsOn's parseDepSpec) so
	// "@owner/slug@^1.2" keeps its owner prefix and drops the constraint.
	if at := strings.LastIndex(ref, "@"); at > 0 {
		ref = ref[:at]
	}
	if strings.HasPrefix(ref, "@") {
		slash := strings.Index(ref, "/")
		if slash < 0 || slash == len(ref)-1 {
			return "", fmt.Errorf("mirrorstack: DependencyDB(%q): owner-prefixed ref must be \"@owner/slug\"", spec)
		}
		ref = ref[slash+1:]
	}
	if ref == "" {
		return "", fmt.Errorf("mirrorstack: DependencyDB(%q): empty module ref", spec)
	}
	if strings.ContainsAny(ref, "/@ \t\r\n\x00") {
		return "", fmt.Errorf("mirrorstack: DependencyDB(%q): module ref %q contains invalid characters", spec, ref)
	}
	return ref, nil
}

// DependencyQuery is the structured read builder. All builder methods
// validate eagerly and latch the FIRST error; Rows/Result surface it without
// touching the network. The builder is single-use and not safe for
// concurrent mutation (build, then execute).
type DependencyQuery struct {
	dep     *Dependency
	table   string
	columns []string
	filters map[string]any
	limit   int
	err     error
}

// Select starts a structured read of one exposed table. name is the LOGICAL
// table name the producer declared via ms.ExposeTable (e.g. "users") —
// lowercase snake_case, never the physical prefixed relation name.
func (d *Dependency) Select(table string) *DependencyQuery {
	q := &DependencyQuery{dep: d, err: d.err}
	q.table = table
	if q.err == nil && !dependencySQLName.MatchString(table) {
		q.err = fmt.Errorf("mirrorstack: DependencyDB.Select(%q): table must be a lowercase snake_case identifier (the logical ExposeTable name)", table)
	}
	return q
}

// setErr latches the first builder error.
func (q *DependencyQuery) setErr(err error) *DependencyQuery {
	if q.err == nil {
		q.err = err
	}
	return q
}

// Columns restricts the projection to the named columns. Accumulates across
// calls; at most 64 names. An empty projection is resolved by the read-exposed
// PROXY only, which expands it to all visible columns; the deployed plane and a
// co-located dev read both require at least one column — the blessed
// dynamic-SELECT builder never emits SELECT *.
func (q *DependencyQuery) Columns(cols ...string) *DependencyQuery {
	if q.err != nil {
		return q
	}
	for _, c := range cols {
		if !dependencySQLName.MatchString(c) {
			return q.setErr(fmt.Errorf("mirrorstack: DependencyDB.Columns(%q): column must be a lowercase snake_case identifier", c))
		}
	}
	if len(q.columns)+len(cols) > maxDependencyColumns {
		return q.setErr(fmt.Errorf("mirrorstack: DependencyDB.Columns: at most %d columns per read", maxDependencyColumns))
	}
	q.columns = append(q.columns, cols...)
	return q
}

// Where adds an equality predicate: col = value. value must be a JSON
// scalar (string, bool, or a numeric type) — the proxy's filter language is
// exactly scalar equality and scalar IN, nothing else (no NULL matching, no
// ranges, no nesting). Predicates across columns are ANDed; at most 16
// filter columns, one predicate per column.
func (q *DependencyQuery) Where(col string, value any) *DependencyQuery {
	if q.err != nil {
		return q
	}
	if err := dependencyScalar("Where", col, value); err != nil {
		return q.setErr(err)
	}
	return q.addFilter("Where", col, value)
}

// WhereIn adds an IN predicate: col IN (values...). 1..200 JSON scalar
// values; an empty list is rejected (an always-false predicate is almost
// certainly a bug — fail fast rather than return silently empty rows).
func (q *DependencyQuery) WhereIn(col string, values ...any) *DependencyQuery {
	if q.err != nil {
		return q
	}
	if len(values) == 0 {
		return q.setErr(fmt.Errorf("mirrorstack: DependencyDB.WhereIn(%q): needs at least one value", col))
	}
	if len(values) > maxDependencyInValues {
		return q.setErr(fmt.Errorf("mirrorstack: DependencyDB.WhereIn(%q): at most %d values", col, maxDependencyInValues))
	}
	for _, v := range values {
		if err := dependencyScalar("WhereIn", col, v); err != nil {
			return q.setErr(err)
		}
	}
	// Copy: the variadic backing array belongs to the caller.
	return q.addFilter("WhereIn", col, append([]any(nil), values...))
}

// addFilter validates the column name, the one-predicate-per-column rule,
// and the filter-count cap shared by Where and WhereIn.
func (q *DependencyQuery) addFilter(op, col string, value any) *DependencyQuery {
	if !dependencySQLName.MatchString(col) {
		return q.setErr(fmt.Errorf("mirrorstack: DependencyDB.%s(%q): column must be a lowercase snake_case identifier", op, col))
	}
	if _, dup := q.filters[col]; dup {
		return q.setErr(fmt.Errorf("mirrorstack: DependencyDB.%s(%q): column already has a filter (one predicate per column)", op, col))
	}
	if len(q.filters) >= maxDependencyFilters {
		return q.setErr(fmt.Errorf("mirrorstack: DependencyDB.%s(%q): at most %d filter columns per read", op, col, maxDependencyFilters))
	}
	if q.filters == nil {
		q.filters = make(map[string]any)
	}
	q.filters[col] = value
	return q
}

// dependencyScalar accepts exactly the JSON scalars the proxy accepts:
// string, bool, and numeric types. nil, time.Time, structs, maps, and
// slices are rejected — format them to a string/number at the call site.
func dependencyScalar(op, col string, v any) error {
	switch v.(type) {
	case string, bool,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64, json.Number:
		return nil
	}
	return fmt.Errorf("mirrorstack: DependencyDB.%s(%q): unsupported filter value type %T (string, bool, or number)", op, col, v)
}

// Limit caps the row count. The platform defaults to 200 when unset (or
// <= 0) and hard-caps at 2000; a read cut at the limit reports
// Truncated=true via Result.
func (q *DependencyQuery) Limit(n int) *DependencyQuery {
	if q.err != nil {
		return q
	}
	q.limit = n
	return q
}

// DependencyResult is one executed read: the decoded rows plus whether the
// read was cut at the limit (more rows exist). Rows is never nil.
type DependencyResult struct {
	Rows      []map[string]any
	Truncated bool
}

// Rows executes the read and returns the decoded rows — one map per row,
// column name to value. Numeric values decode as json.Number (not float64)
// so int64 join keys keep full fidelity for the fetch-then-join in app code.
// Rows is never nil on success. When the read hits the limit the extra rows
// are simply absent here — use Result if you need the Truncated flag.
func (q *DependencyQuery) Rows(ctx context.Context) ([]map[string]any, error) {
	res, err := q.Result(ctx)
	if err != nil {
		return nil, err
	}
	return res.Rows, nil
}

// Result executes the read and returns rows plus the Truncated flag.
// Failure modes are typed and fail-closed (never silently empty) — see the
// Err* sentinels.
func (q *DependencyQuery) Result(ctx context.Context) (*DependencyResult, error) {
	// The plane is a PER-REQUEST property: did THIS invoke arrive through the
	// deployed Lambda envelope? auth.PayloadTrusted is that signal — set by
	// runtime.NewLambdaHandler for both real Lambda AND the dev lambda-invoke
	// shim, and never for a dev-tunnel HTTP request. Deliberately NOT the
	// process-global runtime.IsLambda(): the local deploy-sim shim serves
	// deployed invokes over HTTP with IsLambda==false (it cannot set
	// AWS_LAMBDA_FUNCTION_NAME without lambda.Start() hijacking the process), so
	// keying on IsLambda would wrongly divert the shim to the dev-tunnel proxy.
	return q.result(ctx, auth.PayloadTrusted(ctx))
}

// readExposedRequest mirrors the dispatch read-exposed wire envelope.
type readExposedRequest struct {
	Module   string         `json:"module"`
	Producer string         `json:"producer"`
	Table    string         `json:"table"`
	Columns  []string       `json:"columns,omitempty"`
	Filters  map[string]any `json:"filters,omitempty"`
	Limit    int            `json:"limit,omitempty"`
}

// result is the test seam behind Result. deployed is TRUE when the invoke
// arrived through the deployed Lambda envelope (auth.PayloadTrusted, set by
// runtime.NewLambdaHandler for BOTH real Lambda and the dev lambda-invoke
// shim) — a PER-REQUEST property, injected here so the plane choice is testable
// and never keys on the process-global runtime.IsLambda() (which is false
// inside the local shim's HTTP server). deployed=false is the dev-tunnel path.
func (q *DependencyQuery) result(ctx context.Context, deployed bool) (*DependencyResult, error) {
	if q.err != nil {
		return nil, q.err
	}
	if deployed {
		return q.resultDeployed(ctx)
	}
	// Local-first: when the producer runs in this same `mirrorstack dev` session
	// it shares this Postgres and this app schema, so reading it locally is not
	// an optimization — it is the only way to get the RIGHT rows. Sitting BEFORE
	// the MS_INTERNAL_SECRET read below is deliberate: a co-located read must
	// not have to consume a tunnel SECRET to reach a table in its own database.
	//
	// It does still need a request carrying the REAL app id, which today means
	// `mirrorstack dev --tunnel`: with no platform secret configured,
	// platformAuth takes its local-dev bypass and mints a synthetic
	// "local-dev-app" identity that cannot agree with the header-derived app
	// schema, so the app-scope pin in resultLocal refuses. That refusal is
	// correct — two different app scopes must never resolve — so do not
	// "restore" no-tunnel local reads by weakening the pin; see the pin in
	// dependency_local.go for the other half of this reasoning.
	//
	// handled=false means "not co-located" and falls through to the proxy
	// unchanged; see resultLocal's fallthrough rule.
	if res, handled, err := q.resultLocal(ctx); handled {
		return res, err
	}
	// The proxy binds the caller to its live tunnel session by the session's
	// InternalSecret — the exact value the CLI exports as MS_INTERNAL_SECRET
	// (the same seam the module-log ingest rides). Deliberately NOT the
	// MS_PLATFORM_TOKEN hierarchy: that is a different per-session credential
	// for inbound proxy validation, not this session-identity seam.
	secret := os.Getenv("MS_INTERNAL_SECRET")
	if secret == "" {
		return nil, fmt.Errorf("%w: no dev-tunnel session secret (MS_INTERNAL_SECRET is unset — run under `mirrorstack dev --tunnel`)", ErrDependencyUnauthorized)
	}

	payload := readExposedRequest{
		Module:   q.dep.mod.config.ID,
		Producer: q.dep.producer,
		Table:    q.table,
		Columns:  q.columns,
		Filters:  q.filters,
		Limit:    q.limit,
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("mirrorstack: DependencyDB: marshal request: %w", err)
	}
	if len(buf) > maxDependencyBody {
		return nil, fmt.Errorf("mirrorstack: DependencyDB: request envelope exceeds %d bytes (shrink the filter lists)", maxDependencyBody)
	}

	// Same dispatch base + HTTP client every module->dispatch surface uses
	// (ms.Call / ms.Emit / meter) — one transport config, no second seam.
	u := fmt.Sprintf("%s/internal/apps/%s/read-exposed", dispatchBase(), url.PathEscape(q.dep.appID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-MS-Service-Secret", secret)

	resp, err := callHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mirrorstack: DependencyDB: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, mapReadExposedError(resp.StatusCode, body)
	}

	// UseNumber: row values must round-trip int64 keys losslessly for the
	// app-code join; default float64 decoding would corrupt large IDs.
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	var out struct {
		Rows      []map[string]any `json:"rows"`
		Truncated bool             `json:"truncated"`
	}
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("mirrorstack: DependencyDB: decode response: %w", err)
	}
	if out.Rows == nil {
		out.Rows = []map[string]any{}
	}
	return &DependencyResult{Rows: out.Rows, Truncated: out.Truncated}, nil
}

// errDevPlaneOnly is the pre-decision-18 hard error. It is returned ONLY when
// no dependency manifest is present in ctx — an old platform that does not yet
// inject the manifest (the rollout gate: #31 stays 501 until BOTH sides ship).
// #31's crossReadStatus recognizes the deployed rejection by the literal
// "dev-plane only" substring, so that phrase MUST stay in the message.
var errDevPlaneOnly = errors.New("mirrorstack: DependencyDB is dev-plane only — a deployed module reads a co-located producer's exposed tables via mod.DB (GRANT SELECT); cross-plane deployed reads are not supported")

// resultDeployed is the deployed-plane branch (decision 18 §3): resolve the
// producer + physical relation from the platform-injected manifest, then read
// it AS the module's own consumer-role pool inside a READ ONLY tx. The manifest
// is advisory routing only — Postgres enforces the install-time GRANT ceiling
// on the vended connection, so a wrong/forged manifest name cannot over-read
// (42501 → ErrDependencyUnavailable). Every degradation is a typed fail-closed
// sentinel; none returns silent-empty.
//
// That GRANT ceiling is a DEPLOYED-plane property. The co-located dev caller of
// readGrantedRows runs on a superuser pool with no such ceiling and must
// authorize in Go — see dependency_local.go.
func (q *DependencyQuery) resultDeployed(ctx context.Context) (*DependencyResult, error) {
	// Layer-1 fail-closed: manifest-absent + manifest-omission (decision 18 §5).
	manifest := db.DependenciesFrom(ctx)
	if manifest == nil {
		// No manifest injected → old platform. Keep TODAY's hard error so #31
		// stays 501 until PR 1 (platform) also ships. (decision 18 §3 read step 1)
		return nil, errDevPlaneOnly
	}
	grant, ok := lookupDependency(manifest, q.dep.producer)
	if !ok {
		// Ref absent from the manifest: producer uninstalled/yanked or never a
		// declared dependency. Collapses to one anti-probing verdict (§2 inv 10).
		return nil, fmt.Errorf("%w: producer %q is not an available dependency of this module", ErrProducerNotFound, q.dep.producer)
	}
	physical, ok := grant.Tables[q.table]
	if !ok {
		// Table not in the grant: not exposed on the running version, exposure
		// removed, or consent removed — deliberately indistinguishable (§5).
		return nil, fmt.Errorf("%w: %q is not exposed to this module by %q on its running version", ErrNotExposed, q.table, q.dep.producer)
	}

	rows, truncated, err := q.readGrantedRows(ctx, physical)
	if err != nil {
		// Layer-2 fail-closed: physical/grant state at read time (decision 18 §5).
		return nil, err
	}
	if rows == nil {
		rows = []map[string]any{}
	}
	return &DependencyResult{Rows: rows, Truncated: truncated}, nil
}

// readGrantedRows executes an ALREADY-AUTHORIZED read of one physical relation
// and is shared by both non-proxy planes: the deployed plane passes the name the
// platform manifest supplied, the co-located dev plane passes the name
// localPhysicalName derived. Sharing it means there is only one dynamic SELECT,
// one READ ONLY tx and one SQLSTATE mapping that can ever drift.
//
// It performs NO authorization — every caller must have completed its own walk
// first.
//
// The dev cross-module guard does not fire here, and that is REQUIRED rather
// than accidental: guardQuerier wraps db.Querier (db_guard.go:186) while
// db.TxReadOnly hands fn a raw pgx.Tx and queryDynamicSelect calls tx.Query
// directly. Do NOT "fix" this by routing through Module.DB/Module.Tx — the
// guard's moduleTableRe would reject a foreign m<hex>_* name, and circularly
// so, since its own error text tells users to call ms.DependencyDB. db.go:81's
// seedConn is the existing precedent for a documented, deliberate bypass.
func (q *DependencyQuery) readGrantedRows(ctx context.Context, physical string) ([]map[string]any, bool, error) {
	// Schema comes from the trusted envelope (SchemaFrom), never module input;
	// the physical name comes from the manifest (deployed) or from
	// localPhysicalName (co-located dev). The shape-gate + Sanitize in
	// buildDynamicSelect is the last-line defense on both.
	ds := DynamicSelect{
		Schema:  db.SchemaFrom(ctx),
		Table:   physical,
		Columns: q.columns,
		Filters: filtersToSelect(q.filters),
		Limit:   q.limit,
	}

	// Deployed: mod.DB's consumer-role pool — db.CredentialFrom(ctx) →
	// r_<app8>_<consumer>, the same role the install-time GRANT SELECT targets.
	// Dev: the single shared dev pool. resolvePool refcount-pins until release.
	pool, release, err := q.dep.mod.resolvePool(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("mirrorstack: DependencyDB: acquire consumer pool: %w", err)
	}
	defer release()

	var rows []map[string]any
	var truncated bool
	err = db.TxReadOnly(ctx, pool, func(tx pgx.Tx) error {
		var e error
		rows, truncated, e = queryDynamicSelect(ctx, tx, ds)
		return e
	})
	if err != nil {
		return nil, false, mapDeployedReadError(err)
	}
	return rows, truncated, nil
}

// lookupDependency finds the manifest entry for a producer ref. The manifest is
// keyed by the SAME normalized form parseProducerRef yields (the bare slug the
// platform reconstructs from owner/slug — decision 18 §3 step 6), and
// q.dep.producer is already that normalized form. A miss fails closed
// (ErrProducerNotFound), never over-reads — the conformance test locks this
// key contract against drift.
func lookupDependency(manifest []db.DependencyGrant, producer string) (db.DependencyGrant, bool) {
	for _, g := range manifest {
		if g.Ref == producer {
			return g, true
		}
	}
	return db.DependencyGrant{}, false
}

// filtersToSelect converts the builder's column→value map into the ordered
// SelectFilter slice the dynamic SELECT composes. Columns are sorted so the
// generated SQL text + $n numbering are deterministic (AND-order is
// semantically irrelevant); a scalar becomes a one-value equality, an []any a
// multi-value IN.
func filtersToSelect(filters map[string]any) []SelectFilter {
	if len(filters) == 0 {
		return nil
	}
	cols := make([]string, 0, len(filters))
	for c := range filters {
		cols = append(cols, c)
	}
	sort.Strings(cols)
	out := make([]SelectFilter, 0, len(cols))
	for _, c := range cols {
		if vs, ok := filters[c].([]any); ok { // WhereIn stored the []any as-is
			out = append(out, SelectFilter{Column: c, Values: vs})
			continue
		}
		out = append(out, SelectFilter{Column: c, Values: []any{filters[c]}}) // Where scalar
	}
	return out
}

// mapDeployedReadError maps the READ ONLY tx's Postgres errors onto the typed
// sentinels (decision 18 §5 layer 2). 42P01 (producer dropped/renamed the
// relation) and 42501 (GRANT revoked) both mean the dependency is not readable
// right now → ErrDependencyUnavailable, never silent-empty. Anything else
// (incl. 25006, a builder-bug write on the read-only tx) wraps generically —
// still an error, still fail-closed.
func mapDeployedReadError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "42P01": // undefined_table
			return fmt.Errorf("%w: producer relation is not present (SQLSTATE %s)", ErrDependencyUnavailable, pgErr.Code)
		case "42501": // insufficient_privilege
			return fmt.Errorf("%w: read privilege was revoked (SQLSTATE %s)", ErrDependencyUnavailable, pgErr.Code)
		}
	}
	return fmt.Errorf("mirrorstack: DependencyDB: deployed read failed: %w", err)
}

// mapReadExposedError translates the proxy's error envelope
// ({"error":{"code","message"}}) into the typed sentinels. Fail-closed by
// construction: anything unrecognized is still an error, never empty rows.
func mapReadExposedError(status int, body []byte) error {
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	code, msg := "", ""
	if json.Unmarshal(body, &env) == nil {
		code, msg = env.Error.Code, env.Error.Message
	}
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	switch {
	case status == http.StatusUnauthorized:
		return fmt.Errorf("%w: %s (re-establish the dev tunnel session)", ErrDependencyUnauthorized, msg)
	case status == http.StatusForbidden && code == "dependency_unavailable":
		return fmt.Errorf("%w: %s", ErrDependencyUnavailable, msg)
	case status == http.StatusForbidden:
		// read_not_authorized (and any other 403): not exposed by the running
		// version, no consent, or no consumer role — deliberately collapsed.
		return fmt.Errorf("%w: %s", ErrNotExposed, msg)
	case status == http.StatusNotFound && code == "producer_not_found":
		return fmt.Errorf("%w: %s", ErrProducerNotFound, msg)
	case status == http.StatusNotFound || status == http.StatusMethodNotAllowed:
		// No read-exposed route at all: the platform's proxy is disabled
		// (dispatch not configured for cross-DB reads). Fail closed.
		return fmt.Errorf("%w: read proxy is not enabled on this platform (%d: %s)", ErrDependencyUnavailable, status, msg)
	default:
		return fmt.Errorf("mirrorstack: DependencyDB: read-exposed -> %d %s: %s", status, code, msg)
	}
}
