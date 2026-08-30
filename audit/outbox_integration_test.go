//go:build integration

package audit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/auditoutbox"
	"github.com/mirrorstack-ai/app-module-sdk/internal/invocationwire"
)

const auditIntegrationModuleID = "maudittest"

type integrationModuleQuerier struct{ db.Querier }

func rewriteAuditSQL(sql string) string {
	return strings.ReplaceAll(sql, "__MODULE_ID__", auditIntegrationModuleID)
}

func (q integrationModuleQuerier) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return q.Querier.Exec(ctx, rewriteAuditSQL(sql), args...)
}

func (q integrationModuleQuerier) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return q.Querier.Query(ctx, rewriteAuditSQL(sql), args...)
}

func (q integrationModuleQuerier) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return q.Querier.QueryRow(ctx, rewriteAuditSQL(sql), args...)
}

func auditIntegrationProof(t *testing.T) (context.Context, []byte) {
	t.Helper()
	raw, err := os.ReadFile("../invocation/testdata/invocation_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.TrimSpace(raw)
	inv, err := invocationwire.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return invocationwire.WithContextAndProof(context.Background(), inv, raw), raw
}

func TestAuditOutboxUpgradeLeaseFenceAndRecoveryIntegration(t *testing.T) {
	store, err := db.Open(context.Background())
	if err != nil {
		t.Skipf("skipping: cannot connect to postgres: %v", err)
	}
	t.Cleanup(store.Close)

	ctx := context.Background()
	schema := fmt.Sprintf("audit_outbox_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := store.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { _, _ = store.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE") })
	scopedCtx := db.WithSchema(ctx, schema)

	q, release, err := store.Conn(scopedCtx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	moduleQ := integrationModuleQuerier{Querier: q}

	// This is the exact proof-less shape released before durable forwarding.
	if _, err := moduleQ.Exec(ctx, `
		CREATE TABLE __MODULE_ID___audit_outbox (
			id bigserial PRIMARY KEY,
			occurred_at timestamptz NOT NULL DEFAULT now(),
			subject_kind text NOT NULL,
			subject_id text NOT NULL,
			action text NOT NULL,
			details jsonb,
			delivered_at timestamptz
		);
		INSERT INTO __MODULE_ID___audit_outbox
			(subject_kind, subject_id, action)
		VALUES ('user', 'legacy', 'updated')`); err != nil {
		t.Fatalf("seed legacy outbox: %v", err)
	}

	// Provisioning can race during install retries. The advisory transaction
	// lock must leave one complete, idempotent upgraded table.
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- db.Tx(scopedCtx, store.Pool(), func(txQ db.Querier) error {
				return EnsureTable(scopedCtx, integrationModuleQuerier{Querier: txQ})
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent EnsureTable: %v", err)
		}
	}
	if err := EnsureTable(ctx, moduleQ); err != nil {
		t.Fatalf("idempotent EnsureTable: %v", err)
	}

	var legacyCode string
	var legacyQuarantined bool
	if err := moduleQ.QueryRow(ctx, `
		SELECT last_error_code, quarantined_at IS NOT NULL
		FROM __MODULE_ID___audit_outbox WHERE subject_id = 'legacy'`).Scan(&legacyCode, &legacyQuarantined); err != nil {
		t.Fatalf("read upgraded legacy row: %v", err)
	}
	if legacyCode != "missing_invocation_proof" || !legacyQuarantined {
		t.Fatalf("legacy row code=%q quarantined=%v", legacyCode, legacyQuarantined)
	}

	proofCtx, proof := auditIntegrationProof(t)
	if err := Record(proofCtx, moduleQ, Entry{
		SubjectKind: "user", SubjectID: "new", Action: "updated",
		Details: map[string]any{"field": "displayName"},
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	var storedProof []byte
	if err := moduleQ.QueryRow(ctx, `
		SELECT invocation_proof FROM __MODULE_ID___audit_outbox
		WHERE subject_id = 'new'`).Scan(&storedProof); err != nil {
		t.Fatalf("read proof: %v", err)
	}
	if !bytes.Equal(storedProof, proof) {
		t.Fatal("stored proof differs from authenticated invocation bytes")
	}

	claims := make(chan []auditoutbox.Delivery, 8)
	claimErrs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			workerQ, workerRelease, err := store.Conn(scopedCtx)
			if err != nil {
				claimErrs <- err
				return
			}
			defer workerRelease()
			got, err := auditoutbox.Claim(ctx, integrationModuleQuerier{Querier: workerQ}, 1, time.Now().Add(time.Minute))
			claims <- got
			claimErrs <- err
		}()
	}
	wg.Wait()
	close(claims)
	close(claimErrs)
	for err := range claimErrs {
		if err != nil {
			t.Fatalf("concurrent Claim: %v", err)
		}
	}
	var first auditoutbox.Delivery
	total := 0
	for got := range claims {
		total += len(got)
		if len(got) == 1 {
			first = got[0]
		}
	}
	if total != 1 {
		t.Fatalf("concurrent claims returned %d copies, want exactly one", total)
	}

	if _, err := moduleQ.Exec(ctx, `
		UPDATE __MODULE_ID___audit_outbox
		SET lease_expires_at = clock_timestamp() - interval '1 second'
		WHERE id = $1`, first.ID); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	reclaimed, err := auditoutbox.Claim(ctx, moduleQ, 1, time.Now().Add(time.Minute))
	if err != nil || len(reclaimed) != 1 {
		t.Fatalf("reclaim = %d, %v", len(reclaimed), err)
	}
	if reclaimed[0].LeaseToken == first.LeaseToken {
		t.Fatal("reclaim reused the stale lease fence")
	}
	if err := auditoutbox.Acknowledge(ctx, moduleQ, first.ID, first.LeaseToken); !errors.Is(err, auditoutbox.ErrLeaseLost) {
		t.Fatalf("stale acknowledge = %v, want ErrLeaseLost", err)
	}
	if err := auditoutbox.Acknowledge(ctx, moduleQ, reclaimed[0].ID, reclaimed[0].LeaseToken); err != nil {
		t.Fatalf("current acknowledge: %v", err)
	}

	if err := Record(proofCtx, moduleQ, Entry{SubjectKind: "user", SubjectID: "retry", Action: "updated"}); err != nil {
		t.Fatalf("Record retry row: %v", err)
	}
	retrying, err := auditoutbox.Claim(ctx, moduleQ, 1, time.Now().Add(time.Minute))
	if err != nil || len(retrying) != 1 {
		t.Fatalf("claim retry row = %d, %v", len(retrying), err)
	}
	if err := auditoutbox.Retry(ctx, moduleQ, retrying[0].ID, retrying[0].LeaseToken, "http_503", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if got, err := auditoutbox.Claim(ctx, moduleQ, 1, time.Now().Add(time.Minute)); err != nil || len(got) != 0 {
		t.Fatalf("future retry claim = %d, %v, want none", len(got), err)
	}
	if _, err := moduleQ.Exec(ctx, `
		UPDATE __MODULE_ID___audit_outbox SET available_at = clock_timestamp() - interval '1 second'
		WHERE id = $1`, retrying[0].ID); err != nil {
		t.Fatalf("ready retry row: %v", err)
	}
	retrying, err = auditoutbox.Claim(ctx, moduleQ, 1, time.Now().Add(time.Minute))
	if err != nil || len(retrying) != 1 {
		t.Fatalf("reclaim retry row = %d, %v", len(retrying), err)
	}
	if err := auditoutbox.Quarantine(ctx, moduleQ, retrying[0].ID, retrying[0].LeaseToken, "http_422"); err != nil {
		t.Fatalf("Quarantine: %v", err)
	}
	if got, err := auditoutbox.Claim(ctx, moduleQ, 100, time.Now().Add(time.Minute)); err != nil || len(got) != 0 {
		t.Fatalf("terminal rows remained claimable: %d, %v", len(got), err)
	}
}
