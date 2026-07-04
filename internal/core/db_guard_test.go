package core

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/mirrorstack-ai/app-module-sdk/db"
)

// Realistic platform-minted module IDs (m<32 hex>), matching the physical
// table prefix convention used in app_<id> schemas.
const (
	guardOwnID     = "m81b3ac7081c1409495700c761e23b59e"
	guardForeignID = "m9238a60fa3c943039c28252b454b8071"
)

func TestCheckCrossModuleSQL(t *testing.T) {
	tests := []struct {
		name    string
		sql     string
		wantErr bool
	}{
		{"own table", `SELECT * FROM ` + guardOwnID + `_users WHERE id = $1`, false},
		{"own insert", `INSERT INTO ` + guardOwnID + `_sessions (id) VALUES ($1)`, false},
		{"no module tables", `SELECT 1`, false},
		{"plain table", `SELECT version FROM schema_migrations WHERE scope = $1`, false},
		{"own contributions table", `CREATE TABLE IF NOT EXISTS ` + guardOwnID + `_contributions (slot text)`, false},
		{"own module schema pin", `SET LOCAL search_path TO "mod_` + guardOwnID + `"`, false},

		{"foreign table", `SELECT * FROM ` + guardForeignID + `_users`, true},
		{"foreign join", `SELECT a.id FROM ` + guardOwnID + `_orders a JOIN ` + guardForeignID + `_users u ON u.id = a.user_id`, true},
		{"foreign write", `UPDATE ` + guardForeignID + `_settings SET v = 1`, true},
		{"foreign schema-qualified", `SELECT * FROM app_283e0ef9.` + guardForeignID + `_users`, true},
		{"foreign quoted identifier", `SELECT * FROM "` + guardForeignID + `_users"`, true},
		{"foreign uppercase folds", `SELECT * FROM ` + strings.ToUpper(guardForeignID) + `_USERS`, true},
		{"foreign module schema", `SELECT * FROM mod_` + guardForeignID + `.outbox`, true},

		// Regions where the pattern is meaningless must not trip the guard.
		{"foreign name in string literal", `INSERT INTO ` + guardOwnID + `_logs (msg) VALUES ('saw ` + guardForeignID + `_users today')`, false},
		{"foreign name in escaped literal", `SELECT 'it''s ` + guardForeignID + `_users'`, false},
		{"foreign name in line comment", "SELECT 1 -- " + guardForeignID + "_users\n", false},
		{"foreign name in block comment", `SELECT 1 /* ` + guardForeignID + `_users */`, false},
		{"foreign name in nested block comment", `SELECT 1 /* outer /* ` + guardForeignID + `_users */ still comment */`, false},
		{"foreign name in dollar-quoted string", `SELECT $$` + guardForeignID + `_users$$`, false},
		{"foreign name in tagged dollar-quoted string", `SELECT $body$` + guardForeignID + `_users$body$`, false},

		// The pattern must appear as an identifier start, not mid-word.
		{"foreign id mid-identifier", `SELECT * FROM x` + guardForeignID + `_users`, false},
		{"own table with hex-looking suffix", `SELECT * FROM ` + guardOwnID + `_cache_9238a60fa3c943039c28252b454b8071`, false},

		// A comment must not swallow real SQL after it.
		{"foreign after block comment", `SELECT 1 /* c */ ; SELECT * FROM ` + guardForeignID + `_users`, true},
		{"foreign after literal", `SELECT 'x', u.* FROM ` + guardForeignID + `_users u`, true},
		{"params not dollar quotes", `SELECT * FROM ` + guardForeignID + `_users WHERE a = $1 AND b = $2`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkCrossModuleSQL(guardOwnID, tt.sql)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("checkCrossModuleSQL(%q) = nil, want cross-module error", tt.sql)
				}
				if !errors.Is(err, errCrossModuleRelation) {
					t.Fatalf("error %v is not errCrossModuleRelation", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("checkCrossModuleSQL(%q) = %v, want nil", tt.sql, err)
			}
		})
	}
}

// TestCheckCrossModuleSQLErrorMessage pins the actionable parts of the
// rejection: the offending table, the owning module, and ms.DependencyDB as
// the sanctioned path.
func TestCheckCrossModuleSQLErrorMessage(t *testing.T) {
	err := checkCrossModuleSQL(guardOwnID, `SELECT * FROM `+guardForeignID+`_users`)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	for _, want := range []string{guardForeignID + "_users", guardForeignID, "ms.DependencyDB"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message missing %q:\n%s", want, err.Error())
		}
	}
}

// stubQuerier records whether the underlying Querier was reached.
type stubQuerier struct{ called bool }

func (s *stubQuerier) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	s.called = true
	return pgconn.CommandTag{}, nil
}

func (s *stubQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) {
	s.called = true
	return nil, nil
}

func (s *stubQuerier) QueryRow(context.Context, string, ...any) pgx.Row {
	s.called = true
	return errRow{}
}

func TestGuardQuerierBlocksForeign(t *testing.T) {
	ctx := context.Background()
	foreignSQL := `SELECT * FROM ` + guardForeignID + `_users`

	t.Run("Exec", func(t *testing.T) {
		stub := &stubQuerier{}
		g := guardQuerier{q: stub, ownID: guardOwnID}
		if _, err := g.Exec(ctx, foreignSQL); !errors.Is(err, errCrossModuleRelation) {
			t.Fatalf("Exec err = %v, want errCrossModuleRelation", err)
		}
		if stub.called {
			t.Fatal("foreign Exec reached the underlying querier")
		}
	})

	t.Run("Query", func(t *testing.T) {
		stub := &stubQuerier{}
		g := guardQuerier{q: stub, ownID: guardOwnID}
		if _, err := g.Query(ctx, foreignSQL); !errors.Is(err, errCrossModuleRelation) {
			t.Fatalf("Query err = %v, want errCrossModuleRelation", err)
		}
		if stub.called {
			t.Fatal("foreign Query reached the underlying querier")
		}
	})

	t.Run("QueryRow", func(t *testing.T) {
		stub := &stubQuerier{}
		g := guardQuerier{q: stub, ownID: guardOwnID}
		row := g.QueryRow(ctx, foreignSQL)
		if stub.called {
			t.Fatal("foreign QueryRow reached the underlying querier")
		}
		var v int
		if err := row.Scan(&v); !errors.Is(err, errCrossModuleRelation) {
			t.Fatalf("QueryRow Scan err = %v, want errCrossModuleRelation", err)
		}
	})
}

func TestGuardQuerierPassesOwn(t *testing.T) {
	ctx := context.Background()
	stub := &stubQuerier{}
	g := guardQuerier{q: stub, ownID: guardOwnID}
	if _, err := g.Exec(ctx, `SELECT * FROM `+guardOwnID+`_users`); err != nil {
		t.Fatalf("own-table Exec err = %v, want nil", err)
	}
	if !stub.called {
		t.Fatal("own-table Exec never reached the underlying querier")
	}
}

// TestDevGuardFor pins the dev/prod split: no credential in ctx (the shared
// dev pool) wraps; a platform credential (production / deployed runtime)
// leaves the querier untouched — there the DB role's grants enforce.
func TestDevGuardFor(t *testing.T) {
	m, err := New(Config{ID: guardOwnID})
	if err != nil {
		t.Fatal(err)
	}
	stub := &stubQuerier{}

	devQ := m.devGuardFor(context.Background(), stub, db.CredentialFrom)
	if _, ok := devQ.(guardQuerier); !ok {
		t.Fatalf("dev ctx: got %T, want guardQuerier", devQ)
	}

	prodCtx := db.WithCredential(context.Background(), db.Credential{
		Host: "h", Port: 5432, Database: "d", Username: "u", Token: "t",
	})
	prodQ := m.devGuardFor(prodCtx, stub, db.CredentialFrom)
	if prodQ != db.Querier(stub) {
		t.Fatalf("prod ctx: querier was wrapped (%T), want untouched stub", prodQ)
	}

	// The scope's own credential getter is what decides: a per-app credential
	// must not disable the guard for the module-scope path.
	moduleQ := m.devGuardFor(prodCtx, stub, db.ModuleCredentialFrom)
	if _, ok := moduleQ.(guardQuerier); !ok {
		t.Fatalf("module scope with only app credential: got %T, want guardQuerier", moduleQ)
	}
}
