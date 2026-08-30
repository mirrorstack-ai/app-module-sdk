//go:build integration

package core

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/mirrorstack-ai/app-module-sdk/audit"
	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/invocationwire"
)

func TestTxCommitDrainsAuditAndRollbackDoesNotIntegration(t *testing.T) {
	probe, err := db.Open(context.Background())
	if err != nil {
		t.Skipf("skipping: cannot connect to postgres: %v", err)
	}
	defer probe.Close()

	raw, err := os.ReadFile("../../invocation/testdata/invocation_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.TrimSpace(raw)
	inv, err := invocationwire.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	schema := inv.App.Schema
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	_, _ = probe.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
	if _, err := probe.Exec(context.Background(), "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = probe.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE") })

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	t.Setenv("MS_DISPATCH_URL", server.URL)
	t.Setenv("MS_INTERNAL_SECRET", "audit-service-secret")
	t.Setenv(devMigrateEnvVar, "")

	m, err := New(Config{ID: "mauditintegration", Slug: "audit-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)

	ctx := db.WithSchema(context.Background(), schema)
	ctx = invocationwire.WithContextAndProof(ctx, inv, raw)
	if err := m.Tx(ctx, func(q db.Querier) error { return audit.EnsureTable(ctx, q) }); err != nil {
		t.Fatalf("EnsureTable transaction: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("empty outbox made a transport call")
	}
	if err := m.Tx(ctx, func(q db.Querier) error {
		_, err := q.Exec(ctx, `
			INSERT INTO __MODULE_ID___audit_outbox
				(subject_kind, subject_id, action, invocation_proof)
			VALUES ('user', 'manual', 'updated', $1)`, raw)
		return err
	}); err != nil {
		t.Fatalf("manual outbox transaction: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("transaction without audit.Record triggered an automatic drain")
	}
	if err := m.DrainAudit(ctx); err != nil {
		t.Fatalf("explicit DrainAudit: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("explicit drain calls = %d, want 1", calls.Load())
	}

	if err := m.Tx(ctx, func(q db.Querier) error {
		return audit.Record(ctx, q, audit.Entry{
			SubjectKind: "user", SubjectID: "committed", Action: "updated",
		})
	}); err != nil {
		t.Fatalf("committed transaction: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("post-commit transport calls = %d, want 2 total", calls.Load())
	}

	sentinel := errors.New("rollback")
	err = m.Tx(ctx, func(q db.Querier) error {
		if err := audit.Record(ctx, q, audit.Entry{
			SubjectKind: "user", SubjectID: "rolled-back", Action: "updated",
		}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("rollback error = %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("rollback triggered delivery: calls=%d", calls.Load())
	}

	q, release, err := m.DB(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	var committedDelivered bool
	if err := q.QueryRow(ctx, `
		SELECT delivered_at IS NOT NULL
		FROM __MODULE_ID___audit_outbox WHERE subject_id = 'committed'`).Scan(&committedDelivered); err != nil {
		t.Fatalf("read committed row: %v", err)
	}
	if !committedDelivered {
		t.Fatal("committed audit row was not acknowledged")
	}
	var rolledBack int
	if err := q.QueryRow(ctx, `
		SELECT count(*) FROM __MODULE_ID___audit_outbox WHERE subject_id = 'rolled-back'`).Scan(&rolledBack); err != nil {
		t.Fatalf("read rolled-back row: %v", err)
	}
	if rolledBack != 0 {
		t.Fatalf("rolled-back audit rows = %d, want 0", rolledBack)
	}
}
