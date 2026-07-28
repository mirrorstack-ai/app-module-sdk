package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
)

// testAppID / testAppSchema are ONE app expressed the two ways resultLocal now
// cross-checks: the trusted auth identity's app id, and the schema
// devAppSchemaMiddleware derives from it. They are defined as a derived pair
// rather than two literals so a test can never accidentally assert the
// mismatch the app-scope pin exists to reject — that case gets its own test.
const testAppID = "283e0ef9-1a2b-3c4d-5e6f-0123456789ab"

var testAppSchema = func() string {
	s, ok := devAppSchemaName(testAppID)
	if !ok {
		panic("test app id does not derive a schema")
	}
	return s
}()

// localTestModule wires a module whose dependency directory is a fixed
// in-memory entry and whose registry carries the given declarations, plus an
// app-scoped ctx. The fake lookup is the local plane's ONLY seam, so
// nothing here can reach Postgres — an accidental fallthrough to execution
// surfaces as a hard failure, not a skip. MS_DISPATCH_URL points at an
// unroutable address for the same reason: a case that silently took the proxy
// would fail loudly instead of passing for the wrong reason.
//
// published=true stands in for the normal boot: this module got its own row
// into the directory, so the one-shot self-heal in resultLocal stays dormant
// and cannot reach for a pool. The failed-boot path is covered separately by
// TestPublishDevDirectory_*.
func localTestModule(t *testing.T, entry devModuleEntry, found bool, deps ...registry.Dependency) (*Module, context.Context) {
	t.Helper()
	// Before New(): devMode is captured there, and it is the plane gate.
	t.Setenv(devMigrateEnvVar, "postgres://unused")
	t.Setenv("MS_DISPATCH_URL", "http://127.0.0.1:1")
	t.Setenv("MS_INTERNAL_SECRET", "sess-secret-1")

	m, err := New(Config{ID: "m1234abcd", Slug: "users-profile"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m.devDir.lookup = func(context.Context, string) (devModuleEntry, bool, error) {
		return entry, found, nil
	}
	m.devDir.published.Store(true)
	for _, d := range deps {
		m.registry.AddDependency(d)
	}

	ctx := auth.Set(context.Background(), auth.Identity{AppID: testAppID, UserID: "u1", AppRole: auth.RoleAdmin})
	ctx = db.WithSchema(ctx, testAppSchema)
	return m, ctx
}

// uuidProducerRef is a producer declared by its DASHED UUID — the third ref
// form DependencyDB documents. It is a separate fixture from oauthCoreEntry
// because registry.depIDPattern requires a leading LETTER, so a UUID beginning
// with a digit (as oauth-core's does) is not a legal ms.DependsOn ref at all
// and could never reach authorizeLocalRead. This one is the live app UUID from
// the dev database, which begins with "a".
const uuidProducerRef = "a722a8a8-d413-435b-b21b-f4cbacb5ef73"

func uuidProducerEntry() *devModuleEntry {
	return &devModuleEntry{
		ModuleID: "ma722a8a8d413435bb21bf4cbacb5ef73",
		Slug:     "uuid-producer",
		Exposes:  []string{"users"},
	}
}

// oauthCoreEntry is the canonical co-located producer these tests resolve
// against: oauth-core, exposing exactly "users" — the live shape.
func oauthCoreEntry() devModuleEntry {
	return devModuleEntry{
		ModuleID: "m5dcba905ba6c4242a9c3696f9efc92e9",
		Slug:     "oauth-core",
		Exposes:  []string{"users"},
	}
}

// TestAuthorizeLocalRead drives the pure authorization walk.
//
// Every `deps` entry below is a shape registry.AddDependency would actually
// STORE — checked by the "(declarations are registry-legal)" subtest run
// alongside each case, which feeds it through the real registry. That guard is
// the point: an
// earlier version of this table asserted `{ID: "@mirrorstack/oauth-core@^0.1"}`
// under the name "owner + semver constraint", a shape ValidateDepID PANICS on,
// so the case proved nothing about any reachable state. A constraint lives in
// Dependency.Version, never in Dependency.ID.
func TestAuthorizeLocalRead(t *testing.T) {
	entry := oauthCoreEntry()

	cases := []struct {
		name  string
		deps  []registry.Dependency
		table string
		want  bool
		// entry overrides the default oauthCoreEntry() producer. Needed only by
		// the dashed-UUID cases: registry.depIDPattern requires a LEADING
		// LETTER, so only a UUID whose first hex digit is a-f is a legal
		// DependsOn ref at all, and oauth-core's happens to start with "5".
		entry *devModuleEntry
		// defensive marks a case whose declarations the registry would REJECT.
		// Such a case is testing that authorizeLocalRead stays total over a
		// hand-built slice, not that the state is reachable — and it is exempt
		// from the registry-legality guard below for exactly that reason.
		defensive bool
	}{
		{
			name:  "declared and exposed",
			deps:  []registry.Dependency{{ID: "@mirrorstack/oauth-core", Version: "^0.1", Tables: []string{"users"}}},
			table: "users",
			want:  true,
		},
		{
			name:  "producer undeclared",
			deps:  []registry.Dependency{{ID: "@mirrorstack/billing", Tables: []string{"users"}}},
			table: "users",
			want:  false,
		},
		{
			name:  "declared with no tables at all",
			deps:  []registry.Dependency{{ID: "@mirrorstack/oauth-core"}},
			table: "users",
			want:  false,
		},
		{
			name:  "declared a different table",
			deps:  []registry.Dependency{{ID: "@mirrorstack/oauth-core", Tables: []string{"sessions"}}},
			table: "users",
			want:  false,
		},
		{
			name:  "declared but producer exposes nothing",
			deps:  []registry.Dependency{{ID: "@mirrorstack/oauth-core", Tables: []string{"users"}}},
			table: "sessions", // oauth-core exposes only "users"
			want:  false,
		},
		{
			// The constraint rides in Version — ValidateDepID rejects it in ID.
			name:  "ref form: owner + semver constraint in Version",
			deps:  []registry.Dependency{{ID: "@mirrorstack/oauth-core", Version: "^0.1", Tables: []string{"users"}}},
			table: "users",
			want:  true,
		},
		{
			name:  "ref form: bare slug",
			deps:  []registry.Dependency{{ID: "oauth-core", Tables: []string{"users"}}},
			table: "users",
			want:  true,
		},
		{
			name:  "ref form: raw m<hex> id matches the entry's other identity",
			deps:  []registry.Dependency{{ID: "m5dcba905ba6c4242a9c3696f9efc92e9", Tables: []string{"users"}}},
			table: "users",
			want:  true,
		},
		{
			// DEFECT 4 REGRESSION. The dashed UUID is a documented ref form and
			// ValidateDepID accepts it (all-lowercase-hex + hyphens satisfies
			// depIDPattern). The directory LOOKUP normalizes it to m<hex>, so
			// this declaration resolves to a directory hit and the local plane
			// takes ownership of the read — which means an authorization walk
			// that compared the ref RAW returned "not exposed" for a table that
			// was both declared and exposed. Only the m<hex> case was covered
			// before, which is exactly why the gap survived review.
			name:  "ref form: dashed UUID normalizes to the entry's m<hex> id",
			deps:  []registry.Dependency{{ID: uuidProducerRef, Tables: []string{"users"}}},
			entry: uuidProducerEntry(),
			table: "users",
			want:  true,
		},
		{
			// The same normalization must not manufacture a match: a different
			// UUID is still a different module.
			name:  "ref form: a DIFFERENT dashed UUID still misses",
			deps:  []registry.Dependency{{ID: uuidProducerRef, Tables: []string{"users"}}},
			table: "users",
			want:  false,
		},
		{
			name: "unparseable declaration is skipped, not fatal",
			deps: []registry.Dependency{
				// parseProducerRef rejects this (owner prefix with no slug).
				// ValidateDepID would too, so it is unreachable from the
				// registry — kept because authorizeLocalRead takes a plain
				// slice and must stay total over one.
				{ID: "@nope", Tables: []string{"users"}},
				{ID: "@mirrorstack/oauth-core", Tables: []string{"users"}},
			},
			table:     "users",
			want:      true,
			defensive: true,
		},
		{
			name:  "no declarations at all",
			deps:  nil,
			table: "users",
			want:  false,
		},
	}
	for _, tc := range cases {
		want := entry
		if tc.entry != nil {
			want = *tc.entry
		}
		t.Run(tc.name, func(t *testing.T) {
			if got := authorizeLocalRead(tc.deps, want, tc.table); got != tc.want {
				t.Errorf("authorizeLocalRead = %v, want %v", got, tc.want)
			}
		})

		// The legality guard, run in the SAME loop so a new case cannot be
		// added to the table without passing through it. registry.AddDependency
		// panics on a shape ValidateDepID rejects, so a table entry asserting an
		// impossible declaration fails here instead of quietly overstating what
		// authorizeLocalRead covers.
		if tc.defensive {
			continue
		}
		t.Run(tc.name+" (declarations are registry-legal)", func(t *testing.T) {
			r := registry.New()
			for _, d := range tc.deps {
				r.AddDependency(d)
			}
			// Round-tripping through the registry must not change the verdict:
			// what the registry STORES is what production authorizes against.
			if got := authorizeLocalRead(r.Dependencies(), want, tc.table); got != tc.want {
				t.Errorf("authorizeLocalRead(registry-stored deps) = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDependencyLocal_AuthorizationMissesCollapse is the ANTI-PROBING lock,
// asserted on the error STRING rather than just the sentinel. A consumer must
// not be able to tell "I never declared this producer" from "the producer does
// not expose that table" — otherwise the error shape becomes an oracle for
// enumerating a sibling module's exposure set.
func TestDependencyLocal_AuthorizationMissesCollapse(t *testing.T) {
	entry := oauthCoreEntry()
	noExposure := devModuleEntry{ModuleID: entry.ModuleID, Slug: entry.Slug, Exposes: []string{}}

	cases := []struct {
		name  string
		entry devModuleEntry
		deps  []registry.Dependency
	}{
		{"consumer declared nothing", entry, nil},
		{"consumer declared another producer", entry, []registry.Dependency{{ID: "@mirrorstack/billing", Tables: []string{"users"}}}},
		{"consumer declared the producer but not the table", entry, []registry.Dependency{{ID: "@mirrorstack/oauth-core", Tables: []string{"sessions"}}}},
		{"producer exposes nothing", noExposure, []registry.Dependency{{ID: "@mirrorstack/oauth-core", Tables: []string{"users"}}}},
	}

	var first string
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, ctx := localTestModule(t, tc.entry, true, tc.deps...)

			rows, err := m.DependencyDB(ctx, "@mirrorstack/oauth-core").
				Select("users").Columns("id", "email").Rows(ctx)
			if rows != nil {
				t.Errorf("rows = %v, want nil on an authorization miss (never silently empty)", rows)
			}
			if !errors.Is(err, ErrNotExposed) {
				t.Fatalf("err = %v, want errors.Is(ErrNotExposed)", err)
			}
			if i == 0 {
				first = err.Error()
				return
			}
			if err.Error() != first {
				t.Errorf("message differs between miss shapes — this is a probing oracle:\n first: %s\n  this: %s", first, err.Error())
			}
		})
	}
}

func TestLocalPhysicalName(t *testing.T) {
	const producerID = "m5dcba905ba6c4242a9c3696f9efc92e9"

	got, err := localPhysicalName(producerID, "users")
	if err != nil {
		t.Fatalf("localPhysicalName: %v", err)
	}
	const want = "m5dcba905ba6c4242a9c3696f9efc92e9_users"
	if got != want {
		t.Errorf("localPhysicalName = %q, want %q", got, want)
	}
	if !selectIdentPattern.MatchString(got) {
		t.Errorf("%q does not match selectIdentPattern — buildDynamicSelect would reject it", got)
	}
	// The near-miss that 42P01s: Config.ID ALREADY carries its leading "m".
	if doubled := "m" + producerID + "_users"; got == doubled {
		t.Errorf("localPhysicalName double-prefixed the module id (%q)", doubled)
	}

	// 33-byte id + "_" + a 30-char table = 64 bytes, one over Postgres's budget.
	long := strings.Repeat("a", 30)
	if _, err := localPhysicalName(producerID, long); err == nil {
		t.Errorf("localPhysicalName(%q, 30-char table) = nil error, want the 63-byte budget error", producerID)
	} else if !strings.Contains(err.Error(), "63 bytes") {
		t.Errorf("err = %v, want a message naming the 63-byte budget", err)
	}
}

func TestDirectoryLookupKeys(t *testing.T) {
	cases := []struct {
		ref  string
		want []string
	}{
		{"oauth-core", []string{"oauth-core"}},
		{"m5dcba905ba6c4242a9c3696f9efc92e9", []string{"m5dcba905ba6c4242a9c3696f9efc92e9"}},
		{
			"a722a8a8-d413-435b-b21b-f4cbacb5ef73",
			[]string{"a722a8a8-d413-435b-b21b-f4cbacb5ef73", "ma722a8a8d413435bb21bf4cbacb5ef73"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			got := directoryLookupKeys(tc.ref)
			if len(got) != len(tc.want) {
				t.Fatalf("directoryLookupKeys(%q) = %v, want %v", tc.ref, got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("key[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
	// Never empty: callers pass the result straight to `= ANY($1)`.
	for _, ref := range []string{"", "  ", "not-a-uuid"} {
		if len(directoryLookupKeys(ref)) == 0 {
			t.Errorf("directoryLookupKeys(%q) is empty; it must always carry the verbatim ref", ref)
		}
	}
}

// TestDependencyLocal_InvalidDirectoryModuleID: the row came from another
// process over a shared table, so it is untrusted input. A malformed id is a
// broken local session, not an authorization verdict — plain error, no sentinel.
func TestDependencyLocal_InvalidDirectoryModuleID(t *testing.T) {
	bad := devModuleEntry{ModuleID: "M5DCBA!", Slug: "oauth-core", Exposes: []string{"users"}}
	m, ctx := localTestModule(t, bad, true,
		registry.Dependency{ID: "@mirrorstack/oauth-core", Tables: []string{"users"}})

	rows, err := m.DependencyDB(ctx, "@mirrorstack/oauth-core").
		Select("users").Columns("id").Rows(ctx)
	if rows != nil {
		t.Errorf("rows = %v, want nil", rows)
	}
	if err == nil || !strings.Contains(err.Error(), "invalid module id") {
		t.Fatalf("err = %v, want an invalid-module-id error", err)
	}
	assertNoDependencySentinel(t, err)
}

// TestDependencyLocal_EmptyProjection locks the §5 decision: an empty
// projection is a PROXY-only form. Locally it can neither be composed
// (buildDynamicSelect refuses SELECT *) nor safely expanded (the proxy's
// information_schema probe is an authorization signal only under a restricted
// role, which dev does not have), and falling through would return prod rows.
func TestDependencyLocal_EmptyProjection(t *testing.T) {
	m, ctx := localTestModule(t, oauthCoreEntry(), true,
		registry.Dependency{ID: "@mirrorstack/oauth-core", Tables: []string{"users"}})

	rows, err := m.DependencyDB(ctx, "@mirrorstack/oauth-core").Select("users").Rows(ctx)
	if rows != nil {
		t.Errorf("rows = %v, want nil", rows)
	}
	if err == nil || !strings.Contains(err.Error(), "explicit projection") {
		t.Fatalf("err = %v, want the explicit-projection error", err)
	}
	assertNoDependencySentinel(t, err)
	// errDevPlaneOnly's literal substring is a load-bearing recognition seam for
	// the consumer's 501 mapping. The local branch must never emit it.
	if strings.Contains(err.Error(), "dev-plane only") {
		t.Errorf("local error contains \"dev-plane only\", which #31 string-matches for its 501: %v", err)
	}
}

// TestDependencyLocal_MissingSchema: three app schemas share one database under
// one superuser, so the schema binding is the whole tenant-isolation story. No
// app scope must be a loud error, never a guess and never a default.
func TestDependencyLocal_MissingSchema(t *testing.T) {
	m, _ := localTestModule(t, oauthCoreEntry(), true,
		registry.Dependency{ID: "@mirrorstack/oauth-core", Tables: []string{"users"}})

	// auth identity (so the builder gets its app scope) but NO db.WithSchema.
	ctx := auth.Set(context.Background(), auth.Identity{AppID: "app-uuid-1", UserID: "u1", AppRole: auth.RoleAdmin})

	rows, err := m.DependencyDB(ctx, "@mirrorstack/oauth-core").
		Select("users").Columns("id").Rows(ctx)
	if rows != nil {
		t.Errorf("rows = %v, want nil", rows)
	}
	if err == nil || !strings.Contains(err.Error(), "X-MS-App-ID") {
		t.Fatalf("err = %v, want an error naming the missing X-MS-App-ID app scope", err)
	}
	assertNoDependencySentinel(t, err)
}

// TestDependencyLocal_BuilderErrorStillWinsFirst proves the local check sits
// AFTER the q.err gate: eager builder validation is not weakened by the new
// branch, and a malformed query never reaches the directory.
func TestDependencyLocal_BuilderErrorStillWinsFirst(t *testing.T) {
	m, ctx := localTestModule(t, oauthCoreEntry(), true,
		registry.Dependency{ID: "@mirrorstack/oauth-core", Tables: []string{"users"}})
	m.devDir.lookup = func(context.Context, string) (devModuleEntry, bool, error) {
		t.Fatal("directory consulted despite a latched builder error")
		return devModuleEntry{}, false, nil
	}

	_, err := m.DependencyDB(ctx, "@mirrorstack/oauth-core").
		Select("users").Columns("not-a-valid-col").Rows(ctx)
	if err == nil || !strings.Contains(err.Error(), "column must be") {
		t.Errorf("err = %v, want the builder's column validation error", err)
	}
}

// TestNormalizeLocalRows is the highest-value new test: it locks the row shapes
// a co-located read hands the consumer to the shapes the proxy already hands it.
// The uuid case is the one that silently corrupts a join key when wrong.
func TestNormalizeLocalRows(t *testing.T) {
	ts := time.Date(2026, 7, 20, 12, 30, 0, 0, time.UTC)

	var num pgtype.Numeric
	if err := num.Scan("1.5"); err != nil {
		t.Fatalf("seed pgtype.Numeric: %v", err)
	}

	in := []map[string]any{{
		"id":         [16]byte{0x12, 0x33, 0xb3, 0xf5, 0x31, 0x52, 0x49, 0xc3, 0xb3, 0xbf, 0x6c, 0xd6, 0x5d, 0x87, 0x0a, 0x47},
		"created_at": ts,
		"big":        int64(9007199254740993),
		"amount":     num,
		"deleted_at": nil,
		"email":      "a@b.c",
		"active":     true,
		"meta":       map[string]any{"k": "v"},
		"tags":       []any{"x", "y"},
	}}

	out, err := normalizeLocalRows(in)
	if err != nil {
		t.Fatalf("normalizeLocalRows: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("rows = %d, want 1", len(out))
	}
	row := out[0]

	// uuid: [16]byte would fmt.Sprint as "[18 51 179 ...]" and json.Marshal as
	// the array [18,51,...] — both silently wrong join keys.
	if got, want := row["id"], "1233b3f5-3152-49c3-b3bf-6cd65d870a47"; got != want {
		t.Errorf("id = %#v, want the canonical uuid string %q", got, want)
	}
	if _, ok := row["created_at"].(string); !ok {
		t.Errorf("created_at = %T, want string (the proxy's RFC3339 form)", row["created_at"])
	}
	// int64 must survive as json.Number: float64 would round 9007199254740993
	// down to ...992.
	big, ok := row["big"].(json.Number)
	if !ok {
		t.Fatalf("big = %T, want json.Number", row["big"])
	}
	if big.String() != "9007199254740993" {
		t.Errorf("big = %s, want 9007199254740993 (no float64 rounding)", big)
	}
	// pgtype.Numeric is exactly the type a hand-rolled switch would miss; it
	// marshals correctly only because it implements json.Marshaler.
	amount, ok := row["amount"].(json.Number)
	if !ok {
		t.Fatalf("amount = %T (%#v), want json.Number", row["amount"], row["amount"])
	}
	if amount.String() != "1.5" {
		t.Errorf("amount = %s, want 1.5", amount)
	}
	if row["deleted_at"] != nil {
		t.Errorf("deleted_at = %#v, want nil", row["deleted_at"])
	}
	if row["email"] != "a@b.c" || row["active"] != true {
		t.Errorf("scalars changed: email=%#v active=%#v", row["email"], row["active"])
	}
	if m, ok := row["meta"].(map[string]any); !ok || m["k"] != "v" {
		t.Errorf("meta = %#v, want a passed-through map", row["meta"])
	}
	if s, ok := row["tags"].([]any); !ok || len(s) != 2 {
		t.Errorf("tags = %#v, want a passed-through slice", row["tags"])
	}

	// Rows is never nil on success — and a nil input round-trips through
	// json.Marshal/Decode back to nil, so the guard inside the helper is
	// load-bearing rather than defensive.
	empty, err := normalizeLocalRows(nil)
	if err != nil {
		t.Fatalf("normalizeLocalRows(nil): %v", err)
	}
	if empty == nil {
		t.Errorf("normalizeLocalRows(nil) = nil, want a non-nil empty slice")
	}
	if len(empty) != 0 {
		t.Errorf("normalizeLocalRows(nil) = %v, want empty", empty)
	}
}

// assertNoDependencySentinel pins the §9 rows that must stay PLAIN errors: a
// broken local session is not an authorization verdict, and mapping it onto a
// sentinel would send the consumer's route to the wrong HTTP status.
func assertNoDependencySentinel(t *testing.T, err error) {
	t.Helper()
	for _, s := range []error{ErrDependencyUnauthorized, ErrNotExposed, ErrDependencyUnavailable, ErrProducerNotFound} {
		if errors.Is(err, s) {
			t.Errorf("err %v matched sentinel %v; want a plain error", err, s)
		}
	}
}

// ---------------------------------------------------------------------------
// DEFECT 1 — the app-scope pin.
// ---------------------------------------------------------------------------

// TestDependencyLocal_AppScopeMismatchRefused is the TENANT-ISOLATION lock.
//
// app_a722a8a8_..., app_twkpa_edu and app_dev share one database under one
// superuser role, with no per-app role, no RLS and no app_id column — verified
// against the live dev Postgres. The schema bound into the read is therefore
// the ENTIRE cross-app defense, and binding it from only one of the two app
// scopes in context means the read goes wherever the last db.WithSchema pointed
// rather than where the trusted identity says.
//
// The two scopes here disagree: the identity is one app, the schema binding is
// another. Production makes that combination structurally impossible (per-app
// schema plus per-(app,module) role); locally only this check does. The read
// must be refused, not resolved to either side.
func TestDependencyLocal_AppScopeMismatchRefused(t *testing.T) {
	m, _ := localTestModule(t, oauthCoreEntry(), true,
		registry.Dependency{ID: "@mirrorstack/oauth-core", Tables: []string{"users"}})

	// Trusted identity says app A...
	ctx := auth.Set(context.Background(), auth.Identity{AppID: testAppID, UserID: "u1", AppRole: auth.RoleAdmin})
	// ...the schema binding says app B. A forged/divergent X-MS-App-ID is the
	// realistic way this arises.
	const otherSchema = "app_a722a8a8_d413_435b_b21b_f4cbacb5ef73"
	ctx = db.WithSchema(ctx, otherSchema)

	rows, err := m.DependencyDB(ctx, "@mirrorstack/oauth-core").
		Select("users").Columns("id", "email").Rows(ctx)
	if rows != nil {
		t.Errorf("rows = %v, want nil — a cross-app read must never return data", rows)
	}
	if err == nil {
		t.Fatal("err = nil: the read resolved against a schema the trusted app scope does not derive")
	}
	if !strings.Contains(err.Error(), "app scope does not match") {
		t.Fatalf("err = %v, want the app-scope pin's refusal", err)
	}
	// Both schemas named, so the developer can see WHICH two disagreed.
	if !strings.Contains(err.Error(), otherSchema) || !strings.Contains(err.Error(), testAppSchema) {
		t.Errorf("err = %v, want both schemas named", err)
	}
	assertNoDependencySentinel(t, err)
}

// TestDependencyLocal_AppScopeMatchIsNotRefused is the false-positive guard on
// the pin above: the legitimate case — identity and schema derived from the
// same app id through runtime.AppSchemaName — must sail past it and fail later
// (here on the pool, which these tests never provide).
func TestDependencyLocal_AppScopeMatchIsNotRefused(t *testing.T) {
	m, ctx := localTestModule(t, oauthCoreEntry(), true,
		registry.Dependency{ID: "@mirrorstack/oauth-core", Tables: []string{"users"}})

	_, err := m.DependencyDB(ctx, "@mirrorstack/oauth-core").
		Select("users").Columns("id").Rows(ctx)
	if err != nil && strings.Contains(err.Error(), "app scope does not match") {
		t.Fatalf("the pin rejected a matching app scope: %v", err)
	}
}

// ---------------------------------------------------------------------------
// DEFECT 4 — the dashed-UUID false denial, end to end.
// ---------------------------------------------------------------------------

// TestDependencyLocal_DashedUUIDDeclarationAuthorizes walks the whole
// resultLocal path with the ref form that used to be denied. The distinguishing
// assertion is NOT that the read succeeds (there is no pool here) but that the
// failure is no longer ErrNotExposed: getting that far means the walk accepted
// the declaration and moved on to composing the read.
func TestDependencyLocal_DashedUUIDDeclarationAuthorizes(t *testing.T) {
	const producerUUID = uuidProducerRef
	m, ctx := localTestModule(t, *uuidProducerEntry(), true,
		registry.Dependency{ID: producerUUID, Tables: []string{"users"}})

	_, err := m.DependencyDB(ctx, producerUUID).
		Select("users").Columns("id", "email").Rows(ctx)
	if errors.Is(err, ErrNotExposed) {
		t.Fatalf("err = %v, want NOT ErrNotExposed: %q is a documented ref form, was declared, and the producer exposes the table", err, producerUUID)
	}
}

// ---------------------------------------------------------------------------
// DEFECT 6 — the plane gate.
// ---------------------------------------------------------------------------

// TestDependencyLocal_PlaneGateIsDevModeNotEnvVar pins the gate to m.devMode.
//
// The env var alone is strictly weaker: devMode is MS_LOCAL_DB_URL AND not
// Lambda AND not task-worker, and it is the flag that attaches the app-schema
// middleware. A process where the var is set but devMode is false (a task
// worker in a dev session) has no middleware, so the local branch would run
// with nothing having put a schema in ctx — the branch would be firing outside
// the lifecycle its own comment claims to mirror.
func TestDependencyLocal_PlaneGateIsDevModeNotEnvVar(t *testing.T) {
	t.Setenv("MS_DISPATCH_URL", "http://127.0.0.1:1")
	t.Setenv("MS_INTERNAL_SECRET", "sess-secret-1")
	// New() with the var UNSET → devMode false, captured for the module's life.
	t.Setenv(devMigrateEnvVar, "")
	m, err := New(Config{ID: "m1234abcd", Slug: "users-profile"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m.devDir.lookup = func(context.Context, string) (devModuleEntry, bool, error) {
		t.Error("directory consulted in a process where devMode is false")
		return oauthCoreEntry(), true, nil
	}
	m.registry.AddDependency(registry.Dependency{ID: "@mirrorstack/oauth-core", Tables: []string{"users"}})

	// Now the env var appears — as it does for a task worker spawned inside a
	// dev session. The gate must still hold: devMode was decided at New().
	t.Setenv(devMigrateEnvVar, "postgres://unused")

	ctx := auth.Set(context.Background(), auth.Identity{AppID: testAppID, UserID: "u1"})
	ctx = db.WithSchema(ctx, testAppSchema)
	// Unroutable dispatch: this must take the proxy and fail there, proving it
	// left the local branch rather than being served by it.
	if _, err := m.DependencyDB(ctx, "@mirrorstack/oauth-core").
		Select("users").Columns("id").Rows(ctx); err == nil {
		t.Fatal("err = nil, want the proxy's transport failure")
	}
}

// ---------------------------------------------------------------------------
// DEFECT 2 — the boot publish must not silently no-op.
// ---------------------------------------------------------------------------

// devDirFakes replaces both write seams with recorders and returns them.
func devDirFakes(t *testing.T, ensureErr, publishErr error) (*Module, *int, *int) {
	t.Helper()
	t.Setenv(devMigrateEnvVar, "postgres://unused")
	m, err := New(Config{ID: "m1234abcd", Slug: "users-profile"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ensures, publishes := 0, 0
	m.devDir.ensure = func(context.Context) error { ensures++; return ensureErr }
	m.devDir.publish = func(context.Context) error { publishes++; return publishErr }
	return m, &ensures, &publishes
}

// TestPublishDevDirectory_EnsureFailureStillPublishes is the SILENT-NO-OP lock.
//
// Under `mirrorstack dev --all` every module runs the CREATE at the same
// instant, so a failed ensure is overwhelmingly a racing peer that is creating
// the very table we wanted — meaning the table exists and the publish would
// have succeeded. Returning early on ensure turned that transient race into a
// permanent, process-lifetime invisibility: no row, every consumer back on the
// proxy, and the 503 this feature exists to remove comes straight back.
func TestPublishDevDirectory_EnsureFailureStillPublishes(t *testing.T) {
	m, ensures, publishes := devDirFakes(t, errors.New("duplicate key value violates unique constraint \"pg_type_typname_nsp_index\""), nil)

	if err := m.publishDevDirectory(context.Background()); err != nil {
		t.Fatalf("publishDevDirectory = %v, want nil (the publish succeeded)", err)
	}
	if *ensures != 1 {
		t.Errorf("ensure called %d times, want 1", *ensures)
	}
	if *publishes != 1 {
		t.Fatalf("publish called %d times, want 1 — a failed ensure must NOT skip the publish", *publishes)
	}
	if !m.devDir.published.Load() {
		t.Error("published = false after a successful publish")
	}
}

// TestPublishDevDirectory_PublishFailureIsReported: the other half. When the
// publish itself genuinely fails, the module must NOT be recorded as published
// — otherwise the one-shot self-heal is disarmed and the invisibility is
// permanent again by a different route.
func TestPublishDevDirectory_PublishFailureIsReported(t *testing.T) {
	m, _, _ := devDirFakes(t, nil, errors.New("connection refused"))

	if err := m.publishDevDirectory(context.Background()); err == nil {
		t.Fatal("publishDevDirectory = nil, want the publish error")
	}
	if m.devDir.published.Load() {
		t.Error("published = true despite a failed publish")
	}
}

// TestEnsureDevDirectoryPublished_OneShotSelfHeal: a boot-time failure heals on
// the first read rather than waiting for a hand restart — and heals EXACTLY
// once, so a persistently broken local Postgres cannot turn the read path into
// a write attempt per request.
func TestEnsureDevDirectoryPublished_OneShotSelfHeal(t *testing.T) {
	m, _, publishes := devDirFakes(t, nil, nil)
	if m.devDir.published.Load() {
		t.Fatal("published = true before any publish ran")
	}

	for i := 0; i < 5; i++ {
		m.ensureDevDirectoryPublished(context.Background())
	}
	if *publishes != 1 {
		t.Errorf("publish called %d times over 5 reads, want exactly 1 (sync.Once)", *publishes)
	}
	if !m.devDir.published.Load() {
		t.Error("published = false after the self-heal succeeded")
	}

	// Already published → not even one attempt.
	m2, _, publishes2 := devDirFakes(t, nil, nil)
	m2.devDir.published.Store(true)
	m2.ensureDevDirectoryPublished(context.Background())
	if *publishes2 != 0 {
		t.Errorf("publish called %d times for an already-published module, want 0", *publishes2)
	}
}

// TestIsRelationAlreadyExists covers the two SQLSTATEs a concurrent
// CREATE TABLE IF NOT EXISTS can raise. Both mean the relation exists, which is
// the entire postcondition ensureDevDirectory owes its caller.
func TestIsRelationAlreadyExists(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"42P07 duplicate_table", &pgconn.PgError{Code: "42P07"}, true},
		{"23505 catalog unique violation", &pgconn.PgError{Code: "23505"}, true},
		{"wrapped 23505", fmt.Errorf("dev directory: %w", &pgconn.PgError{Code: "23505"}), true},
		{"42501 insufficient_privilege is NOT a race", &pgconn.PgError{Code: "42501"}, false},
		{"08006 connection failure is NOT a race", &pgconn.PgError{Code: "08006"}, false},
		{"non-pg error", errors.New("boom"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRelationAlreadyExists(tc.err); got != tc.want {
				t.Errorf("isRelationAlreadyExists(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DEFECT 3 — a directory hit must mean "co-located RIGHT NOW".
// ---------------------------------------------------------------------------

// TestColocatedProducer_Memoized covers DEFECT 11 and, with the test below, the
// half of DEFECT 3 that lives in this process: the memo must be bounded, or it
// becomes a second staleness window stacked on the lease's.
func TestColocatedProducer_Memoized(t *testing.T) {
	m, ctx := localTestModule(t, oauthCoreEntry(), true)
	calls := 0
	m.devDir.lookup = func(context.Context, string) (devModuleEntry, bool, error) {
		calls++
		return oauthCoreEntry(), true, nil
	}

	for i := 0; i < 10; i++ {
		if _, ok, err := m.colocatedProducer(ctx, "oauth-core"); err != nil || !ok {
			t.Fatalf("colocatedProducer = (%v, %v)", ok, err)
		}
	}
	if calls != 1 {
		t.Errorf("directory queried %d times over 10 reads, want 1", calls)
	}

	// A MISS is cached too — remote producers are the common case and must not
	// pay a round trip per read.
	m.devDir.lookup = func(context.Context, string) (devModuleEntry, bool, error) {
		calls++
		return devModuleEntry{}, false, nil
	}
	for i := 0; i < 5; i++ {
		if _, ok, _ := m.colocatedProducer(ctx, "billing"); ok {
			t.Fatal("ok = true for a miss")
		}
	}
	if calls != 2 {
		t.Errorf("directory queried %d times total, want 2 (one hit + one miss)", calls)
	}
}

// TestColocatedProducer_MemoExpires: the memo is bounded by
// devDirectoryCacheTTL, so a producer that leaves the session is re-checked
// rather than pinned as co-located by this process forever.
func TestColocatedProducer_MemoExpires(t *testing.T) {
	m, ctx := localTestModule(t, oauthCoreEntry(), true)
	live := true
	calls := 0
	m.devDir.lookup = func(context.Context, string) (devModuleEntry, bool, error) {
		calls++
		if !live {
			return devModuleEntry{}, false, nil
		}
		return oauthCoreEntry(), true, nil
	}

	if _, ok, _ := m.colocatedProducer(ctx, "oauth-core"); !ok {
		t.Fatal("first lookup: ok = false")
	}
	// Age the entry past the window without sleeping for it.
	v, _ := m.devDir.cache.Load("oauth-core")
	c := v.(*devDirectoryCached)
	m.devDir.cache.Store("oauth-core", &devDirectoryCached{
		entry: c.entry, ok: c.ok, at: c.at.Add(-devDirectoryCacheTTL - time.Second),
	})

	live = false // the producer stopped
	if _, ok, _ := m.colocatedProducer(ctx, "oauth-core"); ok {
		t.Error("ok = true after the memo expired and the producer left — a stale memo must not keep a read local")
	}
	if calls != 2 {
		t.Errorf("directory queried %d times, want 2 (the expired entry must be refetched)", calls)
	}
}

// TestColocatedProducer_ErrorsAreNotCached: a transient directory failure must
// not pin the consumer to the proxy for the whole cache window.
func TestColocatedProducer_ErrorsAreNotCached(t *testing.T) {
	m, ctx := localTestModule(t, oauthCoreEntry(), true)
	calls := 0
	m.devDir.lookup = func(context.Context, string) (devModuleEntry, bool, error) {
		calls++
		if calls == 1 {
			return devModuleEntry{}, false, errors.New("directory on fire")
		}
		return oauthCoreEntry(), true, nil
	}

	if _, _, err := m.colocatedProducer(ctx, "oauth-core"); err == nil {
		t.Fatal("first lookup: err = nil, want the directory error")
	}
	if _, ok, err := m.colocatedProducer(ctx, "oauth-core"); err != nil || !ok {
		t.Fatalf("second lookup = (%v, %v), want a fresh successful lookup — the error must not have been cached", ok, err)
	}
}

// TestDevDirectoryLeaseClock locks the relationship between the three timing
// constants rather than their literal values, so tuning any of them keeps the
// argument in dev_directory.go's header true.
func TestDevDirectoryLeaseClock(t *testing.T) {
	if devDirectoryTTL <= devDirectoryHeartbeat {
		t.Errorf("TTL %s <= heartbeat %s: a live producer would expire between beats", devDirectoryTTL, devDirectoryHeartbeat)
	}
	if beats := devDirectoryTTL / devDirectoryHeartbeat; beats < 3 {
		t.Errorf("TTL tolerates only %d missed heartbeat(s); want >= 3 so a GC pause or a laptop sleep does not flap a live session onto the proxy", beats)
	}
	if devDirectoryCacheTTL >= devDirectoryHeartbeat {
		t.Errorf("cache TTL %s >= heartbeat %s: the in-process memo would dominate the lease it is caching", devDirectoryCacheTTL, devDirectoryHeartbeat)
	}
}

// ---------------------------------------------------------------------------
// DEFECT 9 — uuid normalization is not top-level only.
// ---------------------------------------------------------------------------

// TestNormalizeLocalRows_NestedUUIDs: pgx decodes a uuid[] column into a []any
// of [16]byte and a composite/jsonb into a map that can hold one at any depth.
// A top-level-only pre-pass leaves exactly the corrupted join key this function
// exists to prevent, one level down.
func TestNormalizeLocalRows_NestedUUIDs(t *testing.T) {
	u1 := [16]byte{0x12, 0x33, 0xb3, 0xf5, 0x31, 0x52, 0x49, 0xc3, 0xb3, 0xbf, 0x6c, 0xd6, 0x5d, 0x87, 0x0a, 0x47}
	u2 := [16]byte{0xa7, 0x22, 0xa8, 0xa8, 0xd4, 0x13, 0x43, 0x5b, 0xb2, 0x1b, 0xf4, 0xcb, 0xac, 0xb5, 0xef, 0x73}
	const s1 = "1233b3f5-3152-49c3-b3bf-6cd65d870a47"
	const s2 = "a722a8a8-d413-435b-b21b-f4cbacb5ef73"

	out, err := normalizeLocalRows([]map[string]any{{
		"member_ids": []any{u1, u2},                           // uuid[]
		"owner":      map[string]any{"id": u1},                // composite / jsonb
		"deep":       []any{map[string]any{"ids": []any{u2}}}, // nested both ways
	}})
	if err != nil {
		t.Fatalf("normalizeLocalRows: %v", err)
	}
	row := out[0]

	ids, ok := row["member_ids"].([]any)
	if !ok || len(ids) != 2 {
		t.Fatalf("member_ids = %#v, want a 2-element slice", row["member_ids"])
	}
	if ids[0] != s1 || ids[1] != s2 {
		t.Errorf("member_ids = %#v, want [%q %q] — a uuid[] must not marshal as arrays of 16 integers", ids, s1, s2)
	}
	owner, ok := row["owner"].(map[string]any)
	if !ok || owner["id"] != s1 {
		t.Errorf("owner = %#v, want {id: %q}", row["owner"], s1)
	}
	deep, ok := row["deep"].([]any)
	if !ok || len(deep) != 1 {
		t.Fatalf("deep = %#v", row["deep"])
	}
	inner, ok := deep[0].(map[string]any)
	if !ok {
		t.Fatalf("deep[0] = %#v, want a map", deep[0])
	}
	innerIDs, ok := inner["ids"].([]any)
	if !ok || len(innerIDs) != 1 || innerIDs[0] != s2 {
		t.Errorf("deep[0].ids = %#v, want [%q]", inner["ids"], s2)
	}
}
