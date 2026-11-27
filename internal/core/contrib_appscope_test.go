package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
)

// TestContributionRoutes_AppScopeSeam_Integration drives the three pieces that
// decide WHERE a contribution row lands, together and for real:
// devAppSchemaMiddleware (X-MS-App-ID -> app_<id> in the request context), the
// /contrib register/unregister handlers, and the dev lazy provisioning that
// creates <prefix>_contributions inside each app schema. It asserts the row
// exists in app_<id>.<prefix>_contributions and in NO other schema, and that
// DELETE with the same header removes it from that same schema.
//
// A regression that scoped only some verbs (or dropped the middleware for
// /contrib) would pass the platform's header-propagation seam test and this
// repo's other suites while contributions silently landed in the wrong schema.
func TestContributionRoutes_AppScopeSeam_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	resetDefault(t)
	dsn, databaseRequired := provisionIntegrationDSN()
	t.Setenv(devMigrateEnvVar, dsn)

	const moduleID = "m4c0ntr1bs3am00000000000000000001"
	m := newTestModuleWithSecret(t, moduleID)
	if !m.devMode {
		t.Fatal("devMode is false — this test must exercise the dev plane")
	}
	m.ProvideSlot(NewContributionSlot[contribTestPayload]("auth-provider"))

	ctx := context.Background()
	pool, release, err := m.resolvePool(ctx)
	requireProvisionPostgres(t, databaseRequired, err)
	defer release()
	requireProvisionPostgres(t, databaseRequired, pool.Ping(ctx))

	const (
		appA = "aaaaaaaa-6666-6666-a666-666666666601"
		appB = "bbbbbbbb-6666-6666-a666-666666666602"
		idA  = "dddddddd-6666-4666-a666-666666666601"
		idB  = "dddddddd-6666-4666-a666-666666666602"
	)
	schemaA, _ := devAppSchemaName(appA)
	schemaB, _ := devAppSchemaName(appB)
	table := moduleID + "_contributions"
	ident := func(s string) string { return pgx.Identifier{s}.Sanitize() }
	cleanup := func() {
		for _, s := range []string{schemaA, schemaB} {
			if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS `+ident(s)+` CASCADE`); err != nil {
				t.Logf("cleanup %s: %v", s, err)
			}
		}
		if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS public.`+ident(table)); err != nil {
			t.Logf("cleanup public leak check: %v", err)
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	// schemasHolding lists every schema where table has a row for contributionID.
	schemasHolding := func(contributionID string) []string {
		rows, err := pool.Query(ctx, `
			SELECT table_schema FROM information_schema.tables
			WHERE table_name = $1 ORDER BY table_schema`, table)
		if err != nil {
			t.Fatalf("list schemas holding %s: %v", table, err)
		}
		var schemas []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatalf("scan: %v", err)
			}
			schemas = append(schemas, s)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatalf("list schemas: %v", err)
		}
		var holding []string
		for _, s := range schemas {
			var n int
			if err := pool.QueryRow(ctx,
				`SELECT count(*) FROM `+ident(s)+`.`+ident(table)+` WHERE contribution_id = $1`,
				contributionID).Scan(&n); err != nil {
				t.Fatalf("count in %s: %v", s, err)
			}
			if n > 0 {
				holding = append(holding, s)
			}
		}
		return holding
	}
	only := func(got []string, want string) bool { return len(got) == 1 && got[0] == want }

	// Both requests carry ONLY X-MS-App-ID: no schema in the context, so the
	// dev middleware is the sole thing deciding the schema.
	send := func(method, contributionID, appID, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/__mirrorstack/contrib/auth-provider/"+contributionID, strings.NewReader(body))
		req.Header.Set("X-MS-Internal-Secret", "secret")
		req.Header.Set(auth.HeaderAppID, appID)
		rec := httptest.NewRecorder()
		m.Router().ServeHTTP(rec, req)
		return rec
	}

	if rec := send(http.MethodPost, idA, appA, `{"label":"A"}`); rec.Code != http.StatusOK {
		t.Fatalf("POST app A status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := send(http.MethodPost, idB, appB, `{"label":"B"}`); rec.Code != http.StatusOK {
		t.Fatalf("POST app B status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := schemasHolding(idA); !only(got, schemaA) {
		t.Errorf("contribution %s lives in %v, want only %s", idA, got, schemaA)
	}
	if got := schemasHolding(idB); !only(got, schemaB) {
		t.Errorf("contribution %s lives in %v, want only %s", idB, got, schemaB)
	}

	// DELETE with the same header removes it from the same schema — and only A's.
	if rec := send(http.MethodDelete, idA, appA, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE app A status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := schemasHolding(idA); len(got) != 0 {
		t.Errorf("after DELETE, contribution %s still in %v", idA, got)
	}
	if got := schemasHolding(idB); !only(got, schemaB) {
		t.Errorf("DELETE for app A disturbed app B's row: %s lives in %v, want only %s", idB, got, schemaB)
	}
}
