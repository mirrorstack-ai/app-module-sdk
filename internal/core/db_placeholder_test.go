package core

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/mirrorstack-ai/app-module-sdk/db"
)

func TestPlaceholderQuerierResolvesToken(t *testing.T) {
	const id = "m81b3ac7081c1409495700c761e23b59e"
	stub := &stubQuerier{}
	p := placeholderQuerier{q: stub, id: id}

	tests := []struct {
		name string
		sql  string
		want string
	}{
		{"table name prefix", `SELECT * FROM __MODULE_ID___users WHERE id = $1`, `SELECT * FROM ` + id + `_users WHERE id = $1`},
		{"multiple occurrences", `CREATE TABLE __MODULE_ID___a (id uuid); CREATE TABLE __MODULE_ID___b (id uuid)`, `CREATE TABLE ` + id + `_a (id uuid); CREATE TABLE ` + id + `_b (id uuid)`},
		{"no placeholder", `SELECT 1`, `SELECT 1`},
		{"schema qualified", `SELECT * FROM app_x.__MODULE_ID___sessions`, `SELECT * FROM app_x.` + id + `_sessions`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := p.resolve(tt.sql); got != tt.want {
				t.Fatalf("resolve(%q) = %q, want %q", tt.sql, got, tt.want)
			}
		})
	}
}

func TestPlaceholderQuerierDelegatesResolvedSQL(t *testing.T) {
	const id = "m81b3ac7081c1409495700c761e23b59e"
	ctx := context.Background()

	t.Run("Exec", func(t *testing.T) {
		stub := &stubQuerier{}
		p := placeholderQuerier{q: stub, id: id}
		if _, err := p.Exec(ctx, `INSERT INTO __MODULE_ID___users (id) VALUES ($1)`); err != nil {
			t.Fatalf("Exec err = %v, want nil", err)
		}
		if !stub.called {
			t.Fatal("Exec never reached the underlying querier")
		}
	})

	t.Run("Query", func(t *testing.T) {
		stub := &stubQuerier{}
		p := placeholderQuerier{q: stub, id: id}
		if _, err := p.Query(ctx, `SELECT * FROM __MODULE_ID___users`); err != nil {
			t.Fatalf("Query err = %v, want nil", err)
		}
		if !stub.called {
			t.Fatal("Query never reached the underlying querier")
		}
	})

	t.Run("QueryRow", func(t *testing.T) {
		stub := &stubQuerier{}
		p := placeholderQuerier{q: stub, id: id}
		_ = p.QueryRow(ctx, `SELECT 1 FROM __MODULE_ID___users`)
		if !stub.called {
			t.Fatal("QueryRow never reached the underlying querier")
		}
	})
}

// recordingQuerier is a db.Querier stub that captures the exact SQL text it
// receives, so a test can tell resolved SQL (real module ID) apart from
// unresolved SQL (literal modulePlaceholderToken).
type recordingQuerier struct{ lastSQL string }

func (r *recordingQuerier) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	r.lastSQL = sql
	return pgconn.CommandTag{}, nil
}

func (r *recordingQuerier) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	r.lastSQL = sql
	return nil, nil
}

func (r *recordingQuerier) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	r.lastSQL = sql
	return errRow{}
}

// TestWithModuleIDIntegratesWithGuard confirms the actual composition order
// of devGuardFor(ctx, withModuleID(q), ...): guardQuerier is the OUTER
// layer and runs first, scanning the raw statement text — which still
// contains the literal modulePlaceholderToken at that point — before
// delegating to placeholderQuerier, which resolves the token last, right
// before the statement reaches the real connection (see withModuleID's doc
// comment in db_placeholder.go). A query against the module's OWN
// placeholder table must therefore both (a) pass the guard while still
// unresolved, and (b) still arrive at the underlying querier fully resolved
// to the real module ID.
func TestWithModuleIDIntegratesWithGuard(t *testing.T) {
	const id = "m81b3ac7081c1409495700c761e23b59e"
	m, err := New(Config{ID: id})
	if err != nil {
		t.Fatal(err)
	}
	rec := &recordingQuerier{}

	// Dev context (no credential) => guard wraps the placeholder resolver.
	q := m.devGuardFor(context.Background(), m.withModuleID(rec), db.CredentialFrom)
	if _, err := q.Exec(context.Background(), `SELECT * FROM __MODULE_ID___users`); err != nil {
		t.Fatalf("own placeholder table Exec err = %v, want nil", err)
	}
	if want := `SELECT * FROM ` + id + `_users`; rec.lastSQL != want {
		t.Fatalf("underlying querier got SQL %q, want resolved %q", rec.lastSQL, want)
	}
	if strings.Contains(rec.lastSQL, modulePlaceholderToken) {
		t.Fatalf("underlying querier still saw the unresolved placeholder token: %q", rec.lastSQL)
	}
}
