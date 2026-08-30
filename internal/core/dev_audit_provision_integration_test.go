package core

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/mirrorstack-ai/app-module-sdk/audit"
	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/invocationwire"
)

// TestAuditOutbox_DevLazyProvisioning_Integration proves the SDK's normal
// first-request dev path is sufficient for an audited mutation. In particular,
// neither the module handler nor test setup provisions the table directly: the
// dev schema middleware must migrate the app and provision the SDK-owned outbox
// before the handler starts its transaction.
func TestAuditOutbox_DevLazyProvisioning_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	resetDefault(t)
	dsn, databaseRequired := provisionIntegrationDSN()
	t.Setenv(devMigrateEnvVar, dsn)
	t.Setenv("MS_INTERNAL_SECRET", "secret")
	t.Setenv("MS_PLATFORM_TOKEN", "")
	t.Setenv("MS_PLATFORM_TOKEN_FILE", "")

	var deliveries atomic.Int32
	ingress := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		deliveries.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer ingress.Close()
	t.Setenv("MS_DISPATCH_URL", ingress.URL)

	ctx := context.Background()
	probe, err := db.New(ctx, dsn)
	requireProvisionPostgres(t, databaseRequired, err)
	t.Cleanup(probe.Close)
	pool := probe.Pool()

	raw, err := os.ReadFile("../../invocation/testdata/invocation_v1.json")
	if err != nil {
		t.Fatalf("read invocation fixture: %v", err)
	}
	trusted, err := invocationwire.Parse(bytes.TrimSpace(raw))
	if err != nil {
		t.Fatalf("parse invocation fixture: %v", err)
	}
	const route = "/internal/audit"
	trusted.Request.Method = http.MethodPost
	trusted.Request.Path = route
	trusted.Routes.CurrentLocal = route
	proof, err := invocationwire.Marshal(trusted)
	if err != nil {
		t.Fatalf("marshal invocation: %v", err)
	}
	header, err := invocationwire.EncodeHeader(proof)
	if err != nil {
		t.Fatalf("encode invocation header: %v", err)
	}

	const canonicalModuleID = "11111111-1111-4111-8111-111111111111"
	sqlFS := fstest.MapFS{
		"sql/app/0001_init.up.sql": &fstest.MapFile{Data: []byte(`
			CREATE TABLE IF NOT EXISTS __MODULE_ID___lazy_audit_items (
				id text PRIMARY KEY
			)`)},
	}
	newAuditingModule := func(subjectID string) *Module {
		t.Helper()
		m, err := New(Config{
			ID: canonicalModuleID, Slug: trusted.Module.Slug,
			Name: "Lazy Audit Test", SQL: sqlFS,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if !m.devMode {
			t.Fatal("devMode is false — the test must exercise lazy dev provisioning")
		}
		m.Internal(func(r chi.Router) {
			r.Post("/audit", func(w http.ResponseWriter, r *http.Request) {
				err := m.Tx(r.Context(), func(q db.Querier) error {
					if _, err := q.Exec(r.Context(),
						`INSERT INTO __MODULE_ID___lazy_audit_items (id) VALUES ($1)`, subjectID); err != nil {
						return err
					}
					return audit.Record(r.Context(), q, audit.Entry{
						SubjectKind: "user", SubjectID: subjectID, Action: "created",
					})
				})
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			})
		})
		t.Cleanup(m.Close)
		return m
	}

	physicalModuleID := "m11111111111141118111111111111111"
	outboxTable := physicalModuleID + "_audit_outbox"
	itemsTable := physicalModuleID + "_lazy_audit_items"
	schema := trusted.App.Schema
	qualified := func(table string) string { return pgx.Identifier{schema, table}.Sanitize() }
	cleanup := func() {
		if _, err := probe.Exec(ctx, `DROP SCHEMA IF EXISTS `+pgx.Identifier{schema}.Sanitize()+` CASCADE`); err != nil {
			t.Logf("cleanup schema %s: %v", schema, err)
		}
		for _, table := range []string{outboxTable, itemsTable} {
			if _, err := probe.Exec(ctx, `DROP TABLE IF EXISTS public.`+pgx.Identifier{table}.Sanitize()); err != nil {
				t.Logf("cleanup public.%s: %v", table, err)
			}
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	invoke := func(m *Module) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, route, nil)
		req.Header.Set(auth.HeaderInternalSecret, "secret")
		req.Header.Set(auth.HeaderAppID, trusted.App.ID)
		req.Header.Set(invocationwire.Header, header)
		rec := httptest.NewRecorder()
		m.Router().ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("audit mutation status = %d, body = %s", rec.Code, rec.Body.String())
		}
	}

	// A fresh module instance models a second dev process/start and re-enters
	// the idempotent provisioning path against the already-provisioned schema.
	invoke(newAuditingModule("first"))
	invoke(newAuditingModule("second"))

	for table, want := range map[string]int{itemsTable: 2, outboxTable: 2} {
		var got int
		if err := pool.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s`, qualified(table))).Scan(&got); err != nil {
			t.Fatalf("count %s.%s: %v", schema, table, err)
		}
		if got != want {
			t.Errorf("%s.%s rows = %d, want %d", schema, table, got, want)
		}
	}
	if got := deliveries.Load(); got != 2 {
		t.Errorf("audit ingress deliveries = %d, want 2", got)
	}

	var publicLeak bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = ANY($1)
		)`, []string{outboxTable, itemsTable}).Scan(&publicLeak); err != nil {
		t.Fatalf("public leak check: %v", err)
	}
	if publicLeak {
		t.Error("lazy dev provisioning leaked a module table into public")
	}
}
