//go:build integration

package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mirrorstack-ai/app-module-sdk/audit"
	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/auditoutbox"
	"github.com/mirrorstack-ai/app-module-sdk/internal/invocationwire"
)

type auditIntegrationCredentialProvider struct {
	credential db.Credential
}

func (p auditIntegrationCredentialProvider) Credential(context.Context) (db.Credential, error) {
	return p.credential, nil
}

func auditDeliveryIntegrationDSN(t *testing.T) (string, bool) {
	t.Helper()
	for _, name := range []string{"MS_LOCAL_DB_URL", "DATABASE_URL"} {
		if dsn := strings.TrimSpace(os.Getenv(name)); dsn != "" {
			return withAuditPoolLimit(t, dsn), true
		}
	}
	return "postgres://mirrorstack:mirrorstack@localhost:5433/mirrorstack?sslmode=disable&pool_max_conns=2", false
}

func withAuditPoolLimit(t *testing.T, dsn string) string {
	t.Helper()
	if strings.Contains(dsn, "://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatalf("parse integration database URL: %v", err)
		}
		query := u.Query()
		query.Set("pool_max_conns", "2")
		u.RawQuery = query.Encode()
		return u.String()
	}
	return strings.TrimSpace(dsn) + " pool_max_conns=2"
}

func auditDeliveryIntegrationContext(t *testing.T, schema string) (context.Context, []byte) {
	t.Helper()
	raw, err := os.ReadFile("../../invocation/testdata/invocation_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.TrimSpace(raw)
	inv, err := invocationwire.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	ctx := db.WithSchema(context.Background(), schema)
	return invocationwire.WithContextAndProof(ctx, inv, raw), raw
}

func auditDeliveryIntegrationCredential(t *testing.T, dsn string) (db.Credential, string) {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse integration database credential: %v", err)
	}
	conn := cfg.ConnConfig
	if conn.Password == "" {
		t.Skip("integration database has no password for a production-shaped credential test")
	}
	sslmode := "require"
	if conn.TLSConfig == nil {
		sslmode = "disable"
	}
	return db.Credential{
		Host: conn.Host, Port: int(conn.Port), Database: conn.Database,
		Username: conn.User, Token: conn.Password,
	}, sslmode
}

func TestAuditDeliveryReleasesDatabaseBeforeHTTPIntegration(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		name := "explicit_drain"
		if automatic {
			name = "automatic_post_commit"
		}
		t.Run(name, func(t *testing.T) {
			testAuditDeliveryReleasesDatabaseBeforeHTTP(t, automatic)
		})
	}
}

func testAuditDeliveryReleasesDatabaseBeforeHTTP(t *testing.T, automatic bool) {
	t.Helper()
	dsn, required := auditDeliveryIntegrationDSN(t)
	probe, err := db.New(context.Background(), dsn)
	if err != nil {
		if required {
			t.Fatalf("configured integration database is unavailable: %v", err)
		}
		t.Skipf("skipping: integration database is unavailable: %v", err)
	}
	t.Cleanup(probe.Close)

	schema := fmt.Sprintf("audit_delivery_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := probe.Exec(context.Background(), "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { _, _ = probe.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE") })

	started := make(chan struct{}, 2)
	unblock := make(chan struct{})
	var unblockOnce sync.Once
	releaseHTTP := func() { unblockOnce.Do(func() { close(unblock) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-unblock
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)
	t.Setenv("MS_LOCAL_DB_URL", dsn)
	t.Setenv("MS_DISPATCH_URL", server.URL)
	t.Setenv("MS_INTERNAL_SECRET", "audit-service-secret")

	m, err := New(Config{ID: "mauditpool", Slug: "audit-pool"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	t.Cleanup(releaseHTTP)
	ctx, proof := auditDeliveryIntegrationContext(t, schema)
	if err := m.Tx(ctx, func(q db.Querier) error { return audit.EnsureTable(ctx, q) }); err != nil {
		t.Fatalf("ensure audit outbox: %v", err)
	}
	if got := m.devDB.Pool().Config().MaxConns; got != 2 {
		t.Fatalf("test pool max connections = %d, want 2", got)
	}

	done := make(chan error, 2)
	if automatic {
		for index := range 2 {
			go func() {
				done <- m.Tx(ctx, func(q db.Querier) error {
					return audit.Record(ctx, q, audit.Entry{
						SubjectKind: "user", SubjectID: fmt.Sprintf("automatic-%d", index), Action: "updated",
					})
				})
			}()
		}
	} else {
		if err := m.Tx(ctx, func(q db.Querier) error {
			_, err := q.Exec(ctx, `
				INSERT INTO __MODULE_ID___audit_outbox
					(subject_kind, subject_id, action, invocation_proof)
				VALUES ('user', 'explicit-0', 'updated', $1),
				       ('user', 'explicit-1', 'updated', $1)`, proof)
			return err
		}); err != nil {
			t.Fatalf("seed explicit audit event: %v", err)
		}
		for range 2 {
			go func() { done <- m.DrainAudit(ctx) }()
		}
	}

	for range 2 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			releaseHTTP()
			t.Fatal("two concurrent audit transports did not start")
		}
	}

	// The ingress is still blocked. Before #206 (v0.4.12), both workers retained
	// their claim connections here and exhausted the default two-connection app
	// pool. Scope sanitation now runs before acquisition, so both healthy claim
	// connections release synchronously and their slots must already be idle
	// when HTTP begins.
	acquiredDuringHTTP := m.devDB.Pool().Stat().AcquiredConns()
	var probeErr error
	if acquiredDuringHTTP == 0 {
		probeCtx, cancel := context.WithTimeout(ctx, time.Second)
		q, release, err := m.DB(probeCtx)
		probeErr = err
		if probeErr == nil {
			var one int
			probeErr = q.QueryRow(probeCtx, "SELECT 1").Scan(&one)
			if probeErr == nil && one != 1 {
				probeErr = fmt.Errorf("SELECT 1 = %d", one)
			}
			release()
		}
		cancel()
	}
	releaseHTTP()

	var deliveryErrors []error
	for range 2 {
		select {
		case deliveryErr := <-done:
			if deliveryErr != nil {
				deliveryErrors = append(deliveryErrors, deliveryErr)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("audit delivery did not finish after HTTP was released")
		}
	}
	if acquiredDuringHTTP != 0 {
		t.Fatalf("audit HTTP started with %d database connections still acquired", acquiredDuringHTTP)
	}
	if probeErr != nil {
		t.Fatalf("unrelated database work starved during audit HTTP: %v", probeErr)
	}
	if err := errors.Join(deliveryErrors...); err != nil {
		t.Fatalf("audit delivery: %v", err)
	}
}

func TestTxReleasesOriginalPoolMappingBeforeAutomaticAuditHTTPIntegration(t *testing.T) {
	dsn, required := auditDeliveryIntegrationDSN(t)
	probe, err := db.New(context.Background(), dsn)
	if err != nil {
		if required {
			t.Fatalf("configured integration database is unavailable: %v", err)
		}
		t.Skipf("skipping: integration database is unavailable: %v", err)
	}
	t.Cleanup(probe.Close)
	credential, sslmode := auditDeliveryIntegrationCredential(t, dsn)

	schema := fmt.Sprintf("audit_tx_pool_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := probe.Exec(context.Background(), "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { _, _ = probe.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE") })

	started := make(chan struct{}, 1)
	unblock := make(chan struct{})
	var unblockOnce sync.Once
	releaseHTTP := func() { unblockOnce.Do(func() { close(unblock) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-unblock
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)
	t.Setenv("MS_DISPATCH_URL", server.URL)
	t.Setenv("MS_INTERNAL_SECRET", "audit-service-secret")
	t.Setenv("MS_MODULE_DB_SSLMODE", sslmode)

	m, err := New(Config{ID: "maudittxpool", Slug: "audit-tx-pool"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	t.Cleanup(releaseHTTP)
	ctx, _ := auditDeliveryIntegrationContext(t, schema)
	ctx = db.WithCredentialProvider(ctx, auditIntegrationCredentialProvider{credential: credential})
	if err := m.Tx(ctx, func(q db.Querier) error { return audit.EnsureTable(ctx, q) }); err != nil {
		t.Fatalf("ensure audit outbox: %v", err)
	}

	var transactionBackendPID int
	done := make(chan error, 1)
	go func() {
		done <- m.Tx(ctx, func(q db.Querier) error {
			if err := q.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&transactionBackendPID); err != nil {
				return err
			}
			return audit.Record(ctx, q, audit.Entry{
				SubjectKind: "user", SubjectID: "pool-mapping", Action: "updated",
			})
		})
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		releaseHTTP()
		t.Fatal("automatic audit transport did not start")
	}

	// An unkeyed renewable provider gives each database scope a private pool and
	// uses pool.Close as its release function. The exact backend used by Tx must
	// therefore be gone before its post-commit HTTP request can reach ingress.
	var transactionPoolStillOpen bool
	inspectErr := probe.Pool().QueryRow(context.Background(), `
		SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid = $1)`, transactionBackendPID).
		Scan(&transactionPoolStillOpen)
	releaseHTTP()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("automatic audit delivery: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("automatic audit delivery did not finish after HTTP was released")
	}
	if inspectErr != nil {
		t.Fatalf("inspect transaction backend: %v", inspectErr)
	}
	if transactionPoolStillOpen {
		t.Fatalf("transaction backend %d remained open during post-commit HTTP", transactionBackendPID)
	}
}

func TestAuditDeliveryStaleFinalizeIsFencedAfterLeaseRotationIntegration(t *testing.T) {
	dsn, required := auditDeliveryIntegrationDSN(t)
	probe, err := db.New(context.Background(), dsn)
	if err != nil {
		if required {
			t.Fatalf("configured integration database is unavailable: %v", err)
		}
		t.Skipf("skipping: integration database is unavailable: %v", err)
	}
	t.Cleanup(probe.Close)

	schema := fmt.Sprintf("audit_fence_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := probe.Exec(context.Background(), "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { _, _ = probe.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE") })

	started := make(chan struct{}, 1)
	unblock := make(chan struct{})
	var unblockOnce sync.Once
	releaseHTTP := func() { unblockOnce.Do(func() { close(unblock) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-unblock
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)
	t.Setenv("MS_LOCAL_DB_URL", dsn)
	t.Setenv("MS_DISPATCH_URL", server.URL)
	t.Setenv("MS_INTERNAL_SECRET", "audit-service-secret")

	m, err := New(Config{ID: "mauditfence", Slug: "audit-fence"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	t.Cleanup(releaseHTTP)
	ctx, proof := auditDeliveryIntegrationContext(t, schema)
	if err := m.Tx(ctx, func(q db.Querier) error { return audit.EnsureTable(ctx, q) }); err != nil {
		t.Fatalf("ensure audit outbox: %v", err)
	}
	if err := m.Tx(ctx, func(q db.Querier) error {
		_, err := q.Exec(ctx, `
			INSERT INTO __MODULE_ID___audit_outbox
				(subject_kind, subject_id, action, invocation_proof)
			VALUES ('user', 'fenced', 'updated', $1)`, proof)
		return err
	}); err != nil {
		t.Fatalf("seed fenced audit event: %v", err)
	}

	drainCtx, cancelDrain := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- m.DrainAudit(drainCtx) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		cancelDrain()
		releaseHTTP()
		t.Fatal("audit transport did not start")
	}

	q, release, err := m.DB(ctx)
	if err != nil {
		cancelDrain()
		releaseHTTP()
		t.Fatalf("open outbox for lease rotation: %v", err)
	}
	var staleToken string
	if err := q.QueryRow(ctx, `
		SELECT lease_token::text
		FROM __MODULE_ID___audit_outbox
		WHERE subject_id = 'fenced'`).Scan(&staleToken); err != nil {
		release()
		cancelDrain()
		releaseHTTP()
		t.Fatalf("read stale lease token: %v", err)
	}
	if _, err := q.Exec(ctx, `
		UPDATE __MODULE_ID___audit_outbox
		SET lease_expires_at = clock_timestamp() - interval '1 second'
		WHERE subject_id = 'fenced'`); err != nil {
		release()
		cancelDrain()
		releaseHTTP()
		t.Fatalf("expire stale lease: %v", err)
	}
	reclaimed, err := auditoutbox.Claim(ctx, q, 1, time.Now().Add(auditLeaseDuration))
	release()
	if err != nil || len(reclaimed) != 1 {
		cancelDrain()
		releaseHTTP()
		t.Fatalf("reclaim fenced event = %d, %v", len(reclaimed), err)
	}
	newToken := reclaimed[0].LeaseToken
	if newToken == "" || newToken == staleToken {
		cancelDrain()
		releaseHTTP()
		t.Fatalf("reclaim did not rotate lease token: stale=%q new=%q", staleToken, newToken)
	}

	// Cancel the original request after transport entered the handler. Finalize
	// must still reacquire through its detached context, then lose to the newer
	// rotating fence instead of mutating that worker's lease.
	cancelDrain()
	releaseHTTP()
	var drainErr error
	select {
	case drainErr = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stale audit drainer did not finish")
	}
	if !errors.Is(drainErr, auditoutbox.ErrLeaseLost) {
		t.Fatalf("stale drain error = %v, want ErrLeaseLost", drainErr)
	}

	q, release, err = m.DB(ctx)
	if err != nil {
		t.Fatalf("reopen outbox after stale finalize: %v", err)
	}
	defer release()
	var (
		storedToken            string
		attempts               int
		delivered, quarantined bool
		lastErrorCode          string
	)
	if err := q.QueryRow(ctx, `
		SELECT lease_token::text, attempts,
		       delivered_at IS NOT NULL, quarantined_at IS NOT NULL,
		       COALESCE(last_error_code, '')
		FROM __MODULE_ID___audit_outbox
		WHERE subject_id = 'fenced'`).Scan(
		&storedToken, &attempts, &delivered, &quarantined, &lastErrorCode,
	); err != nil {
		t.Fatalf("read row after stale finalize: %v", err)
	}
	if storedToken != newToken || attempts != reclaimed[0].Attempts {
		t.Fatalf("new lease changed: token=%q attempts=%d, want token=%q attempts=%d",
			storedToken, attempts, newToken, reclaimed[0].Attempts)
	}
	if delivered || quarantined || lastErrorCode != "" {
		t.Fatalf("stale finalize mutated row: delivered=%v quarantined=%v last_error=%q",
			delivered, quarantined, lastErrorCode)
	}
}

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
