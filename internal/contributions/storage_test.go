package contributions

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// fakeQuerier records every statement and returns a scripted error, so the
// provisioning contract can be pinned without a database.
type fakeQuerier struct {
	stmts []string
	err   error
}

func (f *fakeQuerier) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	f.stmts = append(f.stmts, sql)
	return pgconn.CommandTag{}, f.err
}

func (f *fakeQuerier) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	f.stmts = append(f.stmts, sql)
	return nil, f.err
}

func (f *fakeQuerier) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	f.stmts = append(f.stmts, sql)
	return nil
}

func pgError(code string) error { return &pgconn.PgError{Code: code} }

// TestEnsureTable_SingleExec pins the property the advisory lock depends on:
// pgx only sends an argument-less Exec over the simple protocol, and only then
// does Postgres wrap the whole string in ONE implicit transaction. Split into
// separate Execs, pg_advisory_xact_lock would be released before the CREATE it
// is supposed to guard, silently.
func TestEnsureTable_SingleExec(t *testing.T) {
	s := NewStorage("m9f1c4b2a7d8e4f0ab1c2d3e4f5061728")
	q := &fakeQuerier{}

	if err := s.EnsureTable(context.Background(), q); err != nil {
		t.Fatalf("EnsureTable: %v", err)
	}
	if len(q.stmts) != 1 {
		t.Fatalf("EnsureTable issued %d statements, want 1 — the lock and the CREATEs must share one implicit transaction", len(q.stmts))
	}
	stmt := q.stmts[0]
	for _, want := range []string{
		"pg_advisory_xact_lock(hashtext(current_schema()",
		"CREATE TABLE IF NOT EXISTS",
		"CREATE INDEX IF NOT EXISTS",
	} {
		if !strings.Contains(stmt, want) {
			t.Errorf("statement is missing %q:\n%s", want, stmt)
		}
	}
	// Unqualified: the table lands in whatever app schema search_path resolves
	// to, which is what makes one binary serve many apps.
	if strings.Contains(stmt, "app_") {
		t.Errorf("statement hard-codes a schema:\n%s", stmt)
	}
}

func TestEnsureTable_ToleratesLostCreateRace(t *testing.T) {
	for _, code := range []string{"42P07", "23505"} {
		t.Run(code, func(t *testing.T) {
			s := NewStorage("mtolerate")
			if err := s.EnsureTable(context.Background(), &fakeQuerier{err: pgError(code)}); err != nil {
				t.Errorf("EnsureTable with %s = %v, want nil — the relation exists, which is the whole postcondition", code, err)
			}
		})
	}
	s := NewStorage("mtolerate")
	if err := s.EnsureTable(context.Background(), &fakeQuerier{err: pgError("42501")}); err == nil {
		t.Error("EnsureTable swallowed 42501 — a missing CREATE grant must surface")
	}
}

func TestWithTable(t *testing.T) {
	ctx := context.Background()

	t.Run("no failure: no provisioning", func(t *testing.T) {
		s := NewStorage("mheal")
		q := &fakeQuerier{}
		calls := 0
		if err := s.WithTable(ctx, q, func() error { calls++; return nil }); err != nil {
			t.Fatalf("WithTable: %v", err)
		}
		if calls != 1 {
			t.Errorf("op ran %d times, want 1", calls)
		}
		if len(q.stmts) != 0 {
			t.Errorf("provisioned %d statements on a healthy store, want 0", len(q.stmts))
		}
	})

	t.Run("42P01: provision then retry once", func(t *testing.T) {
		s := NewStorage("mheal")
		q := &fakeQuerier{}
		calls := 0
		err := s.WithTable(ctx, q, func() error {
			calls++
			if calls == 1 {
				return pgError("42P01")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("WithTable: %v", err)
		}
		if calls != 2 {
			t.Errorf("op ran %d times, want 2 (fail, then retry after provisioning)", calls)
		}
		if len(q.stmts) != 1 || !strings.Contains(q.stmts[0], "CREATE TABLE IF NOT EXISTS") {
			t.Errorf("expected exactly one provisioning statement, got %v", q.stmts)
		}
	})

	t.Run("still 42P01 after provisioning: no second retry", func(t *testing.T) {
		s := NewStorage("mheal")
		calls := 0
		err := s.WithTable(ctx, &fakeQuerier{}, func() error { calls++; return pgError("42P01") })
		if err == nil {
			t.Fatal("WithTable = nil, want the persisting error")
		}
		if calls != 2 {
			t.Errorf("op ran %d times, want 2 — the retry is once, not a loop", calls)
		}
	})

	t.Run("other errors pass through untouched", func(t *testing.T) {
		s := NewStorage("mheal")
		q := &fakeQuerier{}
		want := pgError("42501")
		if err := s.WithTable(ctx, q, func() error { return want }); !errors.Is(err, want) {
			t.Errorf("WithTable = %v, want the original error", err)
		}
		if len(q.stmts) != 0 {
			t.Errorf("provisioned on a non-missing-table error: %v", q.stmts)
		}
	})

	t.Run("provisioning failure names the store", func(t *testing.T) {
		s := NewStorage("mheal")
		q := &fakeQuerier{err: pgError("42501")}
		err := s.WithTable(ctx, q, func() error { return pgError("42P01") })
		if err == nil || !strings.Contains(err.Error(), "provision") {
			t.Errorf("WithTable = %v, want a provisioning failure", err)
		}
	})
}
