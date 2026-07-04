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

	"github.com/mirrorstack-ai/app-module-sdk/auth"
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
