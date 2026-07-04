package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/db"
)

// depTestModule builds a module + app-scoped ctx wired at a fake dispatch.
func depTestModule(t *testing.T, srvURL string) (*Module, context.Context) {
	t.Helper()
	t.Setenv("MS_DISPATCH_URL", srvURL)
	t.Setenv("MS_INTERNAL_SECRET", "sess-secret-1")
	m, err := New(Config{ID: "m1234abcd"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := auth.Set(context.Background(), auth.Identity{AppID: "app-uuid-1", UserID: "u1", AppRole: auth.RoleAdmin})
	return m, ctx
}

// fakeReadExposed serves the wire contract: asserts nothing, records the
// request, and replies with the given status + body.
func fakeReadExposed(t *testing.T, status int, respBody string) (*httptest.Server, *http.Request, *[]byte) {
	t.Helper()
	var gotReq http.Request
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = *r.Clone(r.Context())
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(srv.Close)
	return srv, &gotReq, &gotBody
}

func TestDependencyDB_HappyPath(t *testing.T) {
	srv, gotReq, gotBody := fakeReadExposed(t, http.StatusOK,
		`{"rows":[{"id":9007199254740993,"email":"a@b.c","active":true}],"truncated":true}`)
	m, ctx := depTestModule(t, srv.URL)

	res, err := m.DependencyDB(ctx, "@anna/oauth-core").
		Select("users").
		Columns("id", "email").
		Where("status", "active").
		WhereIn("id", 1, 2).
		Limit(500).
		Result(ctx)
	if err != nil {
		t.Fatalf("Result: %v", err)
	}

	// Wire shape: method, path, headers.
	if gotReq.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", gotReq.Method)
	}
	if gotReq.URL.Path != "/internal/apps/app-uuid-1/read-exposed" {
		t.Errorf("path = %q, want /internal/apps/app-uuid-1/read-exposed", gotReq.URL.Path)
	}
	if got := gotReq.Header.Get("X-MS-Service-Secret"); got != "sess-secret-1" {
		t.Errorf("X-MS-Service-Secret = %q, want sess-secret-1", got)
	}
	if got := gotReq.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}

	// Request envelope: consumer=Config.ID, producer stripped to bare slug,
	// equality scalar + IN array filters, projection, limit.
	var body struct {
		Module   string         `json:"module"`
		Producer string         `json:"producer"`
		Table    string         `json:"table"`
		Columns  []string       `json:"columns"`
		Filters  map[string]any `json:"filters"`
		Limit    int            `json:"limit"`
	}
	if err := json.Unmarshal(*gotBody, &body); err != nil {
		t.Fatalf("unmarshal request body: %v (%s)", err, *gotBody)
	}
	if body.Module != "m1234abcd" {
		t.Errorf("module = %q, want m1234abcd (the caller's Config.ID)", body.Module)
	}
	if body.Producer != "oauth-core" {
		t.Errorf("producer = %q, want oauth-core (owner prefix stripped)", body.Producer)
	}
	if body.Table != "users" {
		t.Errorf("table = %q, want users", body.Table)
	}
	if len(body.Columns) != 2 || body.Columns[0] != "id" || body.Columns[1] != "email" {
		t.Errorf("columns = %v, want [id email]", body.Columns)
	}
	if got := body.Filters["status"]; got != "active" {
		t.Errorf("filters.status = %v, want \"active\"", got)
	}
	if in, ok := body.Filters["id"].([]any); !ok || len(in) != 2 {
		t.Errorf("filters.id = %v, want a 2-element array", body.Filters["id"])
	}
	if body.Limit != 500 {
		t.Errorf("limit = %d, want 500", body.Limit)
	}

	// Decoded result: json.Number fidelity + truncated flag.
	if len(res.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(res.Rows))
	}
	if !res.Truncated {
		t.Errorf("truncated = false, want true")
	}
	id, ok := res.Rows[0]["id"].(json.Number)
	if !ok {
		t.Fatalf("id decoded as %T, want json.Number (int64 fidelity)", res.Rows[0]["id"])
	}
	if id.String() != "9007199254740993" {
		t.Errorf("id = %s, want 9007199254740993 (would corrupt as float64)", id)
	}
	if res.Rows[0]["email"] != "a@b.c" || res.Rows[0]["active"] != true {
		t.Errorf("row = %v", res.Rows[0])
	}
}

func TestDependencyDB_EmptyRowsNeverNil(t *testing.T) {
	srv, _, _ := fakeReadExposed(t, http.StatusOK, `{"rows":[],"truncated":false}`)
	m, ctx := depTestModule(t, srv.URL)

	rows, err := m.DependencyDB(ctx, "oauth-core").Select("users").Rows(ctx)
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	if rows == nil {
		t.Fatalf("rows is nil, want non-nil empty slice")
	}
	if len(rows) != 0 {
		t.Errorf("rows = %v, want empty", rows)
	}
}

func TestDependencyDB_ErrorMapping(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr error
	}{
		{
			name:    "401 unauthorized",
			status:  http.StatusUnauthorized,
			body:    `{"error":{"code":"unauthorized","message":"invalid read-exposed credentials"}}`,
			wantErr: ErrDependencyUnauthorized,
		},
		{
			name:    "403 read_not_authorized",
			status:  http.StatusForbidden,
			body:    `{"error":{"code":"read_not_authorized","message":"cross-module read is not authorized"}}`,
			wantErr: ErrNotExposed,
		},
		{
			name:    "403 dependency_unavailable",
			status:  http.StatusForbidden,
			body:    `{"error":{"code":"dependency_unavailable","message":"producer table is not readable"}}`,
			wantErr: ErrDependencyUnavailable,
		},
		{
			name:    "404 producer_not_found",
			status:  http.StatusNotFound,
			body:    `{"error":{"code":"producer_not_found","message":"producer module not found"}}`,
			wantErr: ErrProducerNotFound,
		},
		{
			name:    "404 without envelope = proxy route absent, fail closed",
			status:  http.StatusNotFound,
			body:    "404 page not found",
			wantErr: ErrDependencyUnavailable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _, _ := fakeReadExposed(t, tc.status, tc.body)
			m, ctx := depTestModule(t, srv.URL)

			rows, err := m.DependencyDB(ctx, "oauth-core").Select("users").Rows(ctx)
			if rows != nil {
				t.Errorf("rows = %v, want nil on error (never silently empty)", rows)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want errors.Is(%v)", err, tc.wantErr)
			}
		})
	}
}

func TestDependencyDB_500IsGenericError(t *testing.T) {
	srv, _, _ := fakeReadExposed(t, http.StatusInternalServerError,
		`{"error":{"code":"internal_error","message":"boom"}}`)
	m, ctx := depTestModule(t, srv.URL)

	_, err := m.DependencyDB(ctx, "oauth-core").Select("users").Rows(ctx)
	if err == nil {
		t.Fatalf("err = nil, want error")
	}
	for _, sentinel := range []error{ErrDependencyUnauthorized, ErrNotExposed, ErrDependencyUnavailable, ErrProducerNotFound} {
		if errors.Is(err, sentinel) {
			t.Errorf("500 mapped to sentinel %v; want generic error", sentinel)
		}
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v, want platform message included", err)
	}
}

func TestDependencyDB_ProducerRefParsing(t *testing.T) {
	cases := []struct {
		ref  string
		want string
	}{
		{"@anna/oauth-core", "oauth-core"},
		{"@anna/oauth-core@^1.2.0", "oauth-core"},
		{"oauth-core@^1", "oauth-core"},
		{"oauth-core", "oauth-core"},
		{"m0e37bd82f0f5427a80549b6a5aebd3a8", "m0e37bd82f0f5427a80549b6a5aebd3a8"},
		{"0e37bd82-f0f5-427a-8054-9b6a5aebd3a8", "0e37bd82-f0f5-427a-8054-9b6a5aebd3a8"},
	}
	for _, tc := range cases {
		got, err := parseProducerRef(tc.ref)
		if err != nil {
			t.Errorf("parseProducerRef(%q): %v", tc.ref, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseProducerRef(%q) = %q, want %q", tc.ref, got, tc.want)
		}
	}

	for _, bad := range []string{"", "  ", "@anna", "@anna/", "@", "a b", "a/b"} {
		if got, err := parseProducerRef(bad); err == nil {
			t.Errorf("parseProducerRef(%q) = %q, want error", bad, got)
		}
	}
}

func TestDependencyDB_BuilderValidation(t *testing.T) {
	// No server: every case must fail BEFORE the network.
	m, ctx := depTestModule(t, "http://127.0.0.1:1") // unroutable — a request would error differently

	many := make([]string, 65)
	for i := range many {
		many[i] = "cx"
	}

	cases := []struct {
		name  string
		build func() *DependencyQuery
		frag  string // expected error fragment
	}{
		{
			name:  "empty table",
			build: func() *DependencyQuery { return m.DependencyDB(ctx, "oauth-core").Select("") },
			frag:  "table must be",
		},
		{
			name:  "uppercase table",
			build: func() *DependencyQuery { return m.DependencyDB(ctx, "oauth-core").Select("Users") },
			frag:  "table must be",
		},
		{
			name:  "physical-looking injection table",
			build: func() *DependencyQuery { return m.DependencyDB(ctx, "oauth-core").Select("users; drop table x") },
			frag:  "table must be",
		},
		{
			name:  "bad ref",
			build: func() *DependencyQuery { return m.DependencyDB(ctx, "@anna").Select("users") },
			frag:  "owner-prefixed ref",
		},
		{
			name: "bad column",
			build: func() *DependencyQuery {
				return m.DependencyDB(ctx, "oauth-core").Select("users").Columns("id", "e-mail")
			},
			frag: "column must be",
		},
		{
			name: "too many columns",
			build: func() *DependencyQuery {
				return m.DependencyDB(ctx, "oauth-core").Select("users").Columns(many...)
			},
			frag: "at most 64 columns",
		},
		{
			name: "nil filter value",
			build: func() *DependencyQuery {
				return m.DependencyDB(ctx, "oauth-core").Select("users").Where("status", nil)
			},
			frag: "unsupported filter value",
		},
		{
			name: "struct filter value",
			build: func() *DependencyQuery {
				return m.DependencyDB(ctx, "oauth-core").Select("users").Where("status", struct{ X int }{1})
			},
			frag: "unsupported filter value",
		},
		{
			name: "empty WhereIn",
			build: func() *DependencyQuery {
				return m.DependencyDB(ctx, "oauth-core").Select("users").WhereIn("id")
			},
			frag: "at least one value",
		},
		{
			name: "oversized WhereIn",
			build: func() *DependencyQuery {
				vals := make([]any, 201)
				for i := range vals {
					vals[i] = i
				}
				return m.DependencyDB(ctx, "oauth-core").Select("users").WhereIn("id", vals...)
			},
			frag: "at most 200 values",
		},
		{
			name: "duplicate filter column",
			build: func() *DependencyQuery {
				return m.DependencyDB(ctx, "oauth-core").Select("users").Where("id", 1).WhereIn("id", 2, 3)
			},
			frag: "one predicate per column",
		},
		{
			name: "too many filters",
			build: func() *DependencyQuery {
				q := m.DependencyDB(ctx, "oauth-core").Select("users")
				for i := 0; i < 17; i++ {
					q = q.Where("col_"+string(rune('a'+i)), i)
				}
				return q
			},
			frag: "at most 16 filter columns",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := tc.build().Rows(ctx)
			if err == nil {
				t.Fatalf("err = nil, want builder validation error")
			}
			if rows != nil {
				t.Errorf("rows = %v, want nil", rows)
			}
			if !strings.Contains(err.Error(), tc.frag) {
				t.Errorf("err = %v, want fragment %q", err, tc.frag)
			}
		})
	}
}

func TestDependencyDB_FirstBuilderErrorWins(t *testing.T) {
	m, ctx := depTestModule(t, "http://127.0.0.1:1")

	_, err := m.DependencyDB(ctx, "oauth-core").
		Select("Users").         // first error: bad table
		Columns("also-bad-col"). // would be a second error
		Where("x", struct{}{}).  // and a third
		Rows(ctx)
	if err == nil || !strings.Contains(err.Error(), "table must be") {
		t.Errorf("err = %v, want the FIRST error (bad table)", err)
	}
}

func TestDependencyDB_RequiresAppScope(t *testing.T) {
	srv, _, _ := fakeReadExposed(t, http.StatusOK, `{"rows":[],"truncated":false}`)
	m, _ := depTestModule(t, srv.URL)

	// Context with no auth identity: no app scope.
	_, err := m.DependencyDB(context.Background(), "oauth-core").Select("users").Rows(context.Background())
	if err == nil || !strings.Contains(err.Error(), "app-scoped context") {
		t.Errorf("err = %v, want app-scoped-context error", err)
	}
}

func TestDependencyDB_RequiresTunnelSecret(t *testing.T) {
	srv, _, _ := fakeReadExposed(t, http.StatusOK, `{"rows":[],"truncated":false}`)
	m, ctx := depTestModule(t, srv.URL)
	t.Setenv("MS_INTERNAL_SECRET", "")

	_, err := m.DependencyDB(ctx, "oauth-core").Select("users").Rows(ctx)
	if !errors.Is(err, ErrDependencyUnauthorized) {
		t.Errorf("err = %v, want ErrDependencyUnauthorized (fail closed without the session secret)", err)
	}
}

func TestDependencyDB_LambdaModeFailsFast(t *testing.T) {
	srv, _, _ := fakeReadExposed(t, http.StatusOK, `{"rows":[],"truncated":false}`)
	m, ctx := depTestModule(t, srv.URL)

	q := m.DependencyDB(ctx, "oauth-core").Select("users")
	_, err := q.result(ctx, true /* inLambda */)
	if err == nil || !strings.Contains(err.Error(), "dev-plane only") {
		t.Errorf("err = %v, want dev-plane-only fail-fast", err)
	}
}

func TestDependencyDB_PackageLevelPanicsBeforeInit(t *testing.T) {
	prev := defaultModule
	defaultModule = nil
	t.Cleanup(func() { defaultModule = prev })

	defer func() {
		if r := recover(); r == nil {
			t.Errorf("DependencyDB before Init did not panic")
		}
	}()
	DependencyDB(context.Background(), "oauth-core")
}

// ---------------------------------------------------------------------------
// Deployed plane (decision 18 §3): the inLambda branch reads the injected
// manifest. These cover the §5 sentinel matrix cells that fail closed BEFORE
// touching a pool (manifest-absent, ref-absent, table-absent) plus the
// read-time SQLSTATE mapping. The happy-path read + live 42P01/42501 live in
// dependency_db_deployed_integration_test.go (build tag `integration`).
// ---------------------------------------------------------------------------

// deployedCtx builds an app-scoped context carrying the given dependency
// manifest, as the Lambda invoke shim would (db.WithSchema + db.WithDependencies).
func deployedCtx(manifest []db.DependencyGrant) context.Context {
	ctx := auth.Set(context.Background(), auth.Identity{AppID: "app-uuid-1", UserID: "u1", AppRole: auth.RoleAdmin})
	ctx = db.WithSchema(ctx, "app_283e0ef9_1a2b_3c4d_5e6f_0123456789ab")
	if manifest != nil {
		ctx = db.WithDependencies(ctx, manifest)
	}
	return ctx
}

func TestDependencyDB_DeployedManifestAbsentIsRolloutGate(t *testing.T) {
	m, err := New(Config{ID: "m1234abcd"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := deployedCtx(nil) // no db.WithDependencies → old-platform rollout gate

	_, err = m.DependencyDB(ctx, "@mirrorstack/oauth-core").Select("users").result(ctx, true)
	if err == nil || !strings.Contains(err.Error(), "dev-plane only") {
		t.Errorf("err = %v, want the today's hard error containing \"dev-plane only\" (#31 stays 501)", err)
	}
	// Must carry no typed sentinel — #31 recognizes it only by the substring.
	for _, s := range []error{ErrProducerNotFound, ErrNotExposed, ErrDependencyUnavailable, ErrDependencyUnauthorized} {
		if errors.Is(err, s) {
			t.Errorf("rollout-gate error matched sentinel %v; must be a plain error", s)
		}
	}
}

func TestDependencyDB_DeployedProducerNotInManifest(t *testing.T) {
	m, _ := New(Config{ID: "m1234abcd"})
	// Manifest present but keyed for a DIFFERENT producer → ref absent.
	ctx := deployedCtx([]db.DependencyGrant{
		{Ref: "some-other-mod", Tables: map[string]string{"widgets": "mdeadbeef_widgets"}},
	})

	_, err := m.DependencyDB(ctx, "@mirrorstack/oauth-core").Select("users").result(ctx, true)
	if !errors.Is(err, ErrProducerNotFound) {
		t.Errorf("err = %v, want ErrProducerNotFound (ref absent from manifest)", err)
	}
}

func TestDependencyDB_DeployedTableNotExposed(t *testing.T) {
	m, _ := New(Config{ID: "m1234abcd"})
	// Producer present, but the requested table is not in its exposed set.
	ctx := deployedCtx([]db.DependencyGrant{
		{Ref: "oauth-core", Tables: map[string]string{"sessions": "m81b3ac70_sessions"}},
	})

	_, err := m.DependencyDB(ctx, "@mirrorstack/oauth-core").Select("users").result(ctx, true)
	if !errors.Is(err, ErrNotExposed) {
		t.Errorf("err = %v, want ErrNotExposed (table not in manifest .Tables)", err)
	}
}

func TestMapDeployedReadError(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		want    error // sentinel it must errors.Is; nil = must NOT match any dep sentinel
		matches bool
	}{
		{"42P01 undefined_table", &pgconn.PgError{Code: "42P01"}, ErrDependencyUnavailable, true},
		{"42501 insufficient_privilege", &pgconn.PgError{Code: "42501"}, ErrDependencyUnavailable, true},
		{"25006 read_only_write is generic", &pgconn.PgError{Code: "25006"}, ErrDependencyUnavailable, false},
		{"non-pg error is generic", errors.New("boom"), ErrDependencyUnavailable, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapDeployedReadError(tc.err)
			if got == nil {
				t.Fatalf("mapDeployedReadError returned nil (never swallow a read error)")
			}
			if errors.Is(got, tc.want) != tc.matches {
				t.Errorf("errors.Is(%v, ErrDependencyUnavailable) = %v, want %v", got, errors.Is(got, tc.want), tc.matches)
			}
		})
	}
}

func TestFiltersToSelect_DeterministicSortedOrder(t *testing.T) {
	// Map iteration is unordered; the SELECT's $n numbering must not be. Columns
	// sort ascending; scalars become one-value equality, []any becomes IN.
	got := filtersToSelect(map[string]any{
		"status": "active",
		"id":     []any{1, 2},
		"email":  "a@b.c",
	})
	want := []SelectFilter{
		{Column: "email", Values: []any{"a@b.c"}},
		{Column: "id", Values: []any{1, 2}},
		{Column: "status", Values: []any{"active"}},
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Column != want[i].Column {
			t.Errorf("filter[%d].Column = %q, want %q (must be sorted)", i, got[i].Column, want[i].Column)
		}
	}
}

// TestPlatformRefKeyConformance is the second cross-repo conformance lock
// (decision 18 §7 PR2): the manifest key the platform emits per producer must
// equal the SDK's parseProducerRef normalization of any ref form the consumer
// declares that producer by. The platform reverse-maps producerUUID →
// (owner, slug) and keys by the normalized ref (decision 18 §3 step 6); a
// consumer names the producer "@owner/slug[@constraint]" or bare "slug". If
// these two normalizations drift, the SDK lookup fails closed
// (ErrProducerNotFound) — never over-reads — but the read stops working, so we
// freeze the agreement here.
func TestPlatformRefKeyConformance(t *testing.T) {
	// platformManifestKey models PR1's reconstruction: the manifest entry for a
	// producer is keyed by the bare slug (what parseProducerRef yields for the
	// "@owner/slug" a consumer declares). Modeled independently of
	// parseProducerRef so a change to either surfaces here.
	platformManifestKey := func(owner, slug string) string { return slug }

	const owner, slug = "mirrorstack", "oauth-core"
	key := platformManifestKey(owner, slug)

	// Every slug-bearing consumer ref form must normalize to the platform key.
	slugForms := []string{
		"@" + owner + "/" + slug,           // canonical
		"@" + owner + "/" + slug + "@^0.1", // + version constraint (the real #31 form)
		slug,                               // bare slug
		slug + "@^1",                       // bare + constraint
	}
	for _, ref := range slugForms {
		got, err := parseProducerRef(ref)
		if err != nil {
			t.Errorf("parseProducerRef(%q): %v", ref, err)
			continue
		}
		if got != key {
			t.Errorf("parseProducerRef(%q) = %q, want platform key %q", ref, got, key)
		}
	}

	// ID-form refs (m<hex>, dashed UUID) pass through unchanged: the platform
	// only ever emits slug keys, so a consumer that declares its dependency by
	// ID form fails closed (ErrProducerNotFound) against a slug-keyed manifest.
	// Documented, not a match — locking the pass-through so it can't silently
	// start rewriting IDs into something that spuriously collides with a slug.
	idForms := map[string]string{
		"m0e37bd82f0f5427a80549b6a5aebd3a8":    "m0e37bd82f0f5427a80549b6a5aebd3a8",
		"0e37bd82-f0f5-427a-8054-9b6a5aebd3a8": "0e37bd82-f0f5-427a-8054-9b6a5aebd3a8",
	}
	for ref, want := range idForms {
		got, err := parseProducerRef(ref)
		if err != nil {
			t.Errorf("parseProducerRef(%q): %v", ref, err)
			continue
		}
		if got != want {
			t.Errorf("parseProducerRef(%q) = %q, want %q (unchanged pass-through)", ref, got, want)
		}
		if got == key {
			t.Errorf("ID-form %q normalized to the slug key %q — must NOT collide", ref, key)
		}
	}
}
