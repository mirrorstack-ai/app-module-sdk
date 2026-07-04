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
	"strings"

	"github.com/mirrorstack-ai/app-module-sdk/internal/runtime"
)

// ms.DependencyDB — the RESTRICTED consumer accessor for reading a producer
// module's exposed tables (decision 17 §2, option (d): the SDK client of the
// platform read proxy).
//
// A consumer that declared ms.DependsOn("@owner/producer", n.Table("users"))
// reads those rows through the platform's read-exposed proxy — a STRUCTURED
// read (table + projection + equality/IN filters, never raw SQL) that the
// platform authorizes against the same consent+exposure catalog the
// install-time grant walk uses and executes as the consumer's own
// r_<app8>_<mod> role inside a READ ONLY transaction. There is deliberately
// NO raw SQL surface and NO pool here: the dev plane holds no socket to the
// platform DB, so a cross-plane SQL JOIN is structurally impossible —
// fetch the exposed rows, then join in application code.
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
// DEV-PLANE ONLY today: the proxy authenticates the LIVE dev-tunnel session
// (the CLI-minted MS_INTERNAL_SECRET). A deployed consumer reads a
// co-located producer directly via mod.DB (the GRANT SELECT path); wiring
// DependencyDB for deployed consumers (envelope-vended proxy credentials) is
// a documented follow-up.

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
	consumer string // this module's Config.ID — the proxy verifies it IS the caller
	producer string // producer ref resolved within the same app (slug | UUID | m<hex>)
	appID    string // trusted app scope from ctx (auth identity), never caller-supplied
	err      error  // deferred construction error (bad ref, no app scope)
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
	d := &Dependency{consumer: m.config.ID}
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

// Columns restricts the projection to the named columns (all visible
// columns when never called). Accumulates across calls; at most 64 names.
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
	return q.result(ctx, runtime.IsLambda())
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

// result is the test seam behind Result (inLambda injected so tests don't
// depend on process env captured at package init).
func (q *DependencyQuery) result(ctx context.Context, inLambda bool) (*DependencyResult, error) {
	if q.err != nil {
		return nil, q.err
	}
	if inLambda {
		// The proxy authenticates a LIVE dev-tunnel session; a deployed
		// consumer has none. Deployed->deployed reads use the direct GRANT
		// via mod.DB (decision 17 resolution matrix); vending proxy
		// credentials to deployed consumers is a documented follow-up.
		return nil, errors.New("mirrorstack: DependencyDB is dev-plane only — a deployed module reads a co-located producer's exposed tables via mod.DB (GRANT SELECT); cross-plane deployed reads are not supported")
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
		Module:   q.dep.consumer,
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
