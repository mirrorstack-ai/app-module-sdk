package system

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/migration"
)

// noopTxRunner is a TxRunner stub that fails the test if invoked.
//
// Tests in this file use empty (or near-empty) sqlFS specifically so that
// List/Slice return empty slices and Apply/ApplyDown short-circuit BEFORE
// touching the runner. The stub asserts that contract: if a future refactor
// makes Apply call runTx unconditionally (e.g., to pre-warm state outside
// the slice loop), these tests will fail loudly rather than silently
// exercise an unintended SQL path.
//
// For tests that legitimately need to drive the runner (Apply/ApplyDown
// against a real DB), use integration-tagged tests with a real Postgres,
// not this stub.
func noopTxRunner(t *testing.T) migration.TxRunner {
	return func(ctx context.Context, fn func(q db.Querier) error) error {
		t.Helper()
		t.Errorf("TxRunner unexpectedly called — test assumed empty sqlFS would short-circuit before SQL execution")
		return nil
	}
}

func TestInstallHandler_EmptyFS(t *testing.T) {
	t.Parallel()

	h := InstallHandler(nil, noopTxRunner(t))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/install", nil))

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got LifecycleResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Applied) != 0 {
		t.Errorf("applied = %v, want empty for empty FS", got.Applied)
	}
}

func TestUpgradeHandler_RequiresFromTo(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
	}{
		{"empty body", ""},
		{"missing from", `{"to": "0001"}`},
		{"missing to", `{"from": "0000"}`},
		{"both empty strings", `{"from": "", "to": ""}`},
		{"malformed json", `{from: 0000`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := UpgradeHandler(nil, noopTxRunner(t))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("POST", "/upgrade", strings.NewReader(tc.body)))

			if rec.Code != 400 {
				t.Errorf("status = %d, want 400 for body %q", rec.Code, tc.body)
			}
		})
	}
}

func TestUpgradeHandler_RejectsSemver(t *testing.T) {
	t.Parallel()

	// The platform is responsible for semver→migration translation before
	// calling the SDK — the handler must refuse non-numeric versions rather
	// than silently pass them through. Error message hints at the fix so a
	// caller who wired the pipe wrong sees it immediately.
	cases := []struct {
		name string
		body string
	}{
		{"semver from", `{"from": "v0.1.0", "to": "0008"}`},
		{"semver to", `{"from": "0000", "to": "v0.1.0"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := UpgradeHandler(nil, noopTxRunner(t))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("POST", "/upgrade", strings.NewReader(tc.body)))

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 for semver input", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "migration number") {
				t.Errorf("error body = %q, want hint about migration numbers", rec.Body.String())
			}
		})
	}
}

func TestUpgradeHandler_UnknownTarget(t *testing.T) {
	t.Parallel()

	// Empty sqlFS + valid body → Slice fails because the target version
	// doesn't exist in the (empty) migrations list. The handler must
	// translate the runner's error into a 400 (caller asked for something
	// the module doesn't have).
	h := UpgradeHandler(nil, noopTxRunner(t))
	body := strings.NewReader(`{"from": "0000", "to": "0001"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/upgrade", body))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (unknown target with empty fs)", rec.Code)
	}
}

func TestUpgradeHandler_NoOp(t *testing.T) {
	t.Parallel()

	// from == to (both present in sqlFS) → Slice returns empty → Apply
	// short-circuits before the runner is called → 200 with empty Applied.
	// This exercises the happy-path wiring without needing a real DB.
	fsys := fstest.MapFS{
		"sql/app/0008_a.up.sql": &fstest.MapFile{Data: []byte("")},
	}
	h := UpgradeHandler(fsys, noopTxRunner(t))
	body := strings.NewReader(`{"from": "0008", "to": "0008"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/upgrade", body))

	if rec.Code != 200 {
		t.Errorf("status = %d, want 200 (no-op upgrade)", rec.Code)
	}
	var got LifecycleResult
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Applied) != 0 {
		t.Errorf("applied = %v, want empty (no-op)", got.Applied)
	}
}

func TestDowngradeHandler_RequiresFromGreaterThanTo(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		"sql/app/0001_a.up.sql":   &fstest.MapFile{Data: []byte("")},
		"sql/app/0001_a.down.sql": &fstest.MapFile{Data: []byte("")},
	}
	h := DowngradeHandler(fsys, noopTxRunner(t))

	// from=0001, to=0001 → not a downgrade → 400
	body := strings.NewReader(`{"from": "0001", "to": "0001"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/downgrade", body))

	if rec.Code != 400 {
		t.Errorf("status = %d, want 400 for from <= to", rec.Code)
	}
}

func TestUninstallHandler_AlwaysSucceeds(t *testing.T) {
	t.Parallel()

	h := UninstallHandler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/uninstall", nil))

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got UninstallResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != "ok" {
		t.Errorf("status = %q, want ok", got.Status)
	}
}

func TestInstallHandler_BadFS_500(t *testing.T) {
	t.Parallel()

	// fs.FS that returns an error on every Open. fs.ReadDir wraps and
	// surfaces the error — the handler should respond 500.
	h := InstallHandler(errFS{}, noopTxRunner(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/install", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 for fs read error", rec.Code)
	}
}

// errFS implements fs.FS and returns a non-NotExist error on every Open.
// fs.ErrInvalid is treated as a real error (not the "missing sql/ dir"
// special case the runner handles internally).
type errFS struct{}

func (errFS) Open(name string) (fs.File, error) {
	return nil, fs.ErrInvalid
}
