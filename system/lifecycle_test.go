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
// makes Apply call runTx unconditionally (e.g., to pre-warm the tracking
// table outside the slice loop), these tests will fail loudly rather than
// silently exercise an unintended SQL path.
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

func TestResolveVersion(t *testing.T) {
	t.Parallel()

	versions := map[string]string{"v0.1.0": "0008", "v0.2.0": "0012"}

	if got := resolveVersion(versions, "v0.1.0"); got != "0008" {
		t.Errorf("resolveVersion(v0.1.0) = %q, want 0008", got)
	}
	if got := resolveVersion(versions, "v0.2.0"); got != "0012" {
		t.Errorf("resolveVersion(v0.2.0) = %q, want 0012", got)
	}
	// Pass-through: unknown input returned as-is (callers can pass migration numbers).
	if got := resolveVersion(versions, "0042"); got != "0042" {
		t.Errorf("resolveVersion(0042) = %q, want 0042 (pass-through)", got)
	}
	// Nil map: pass-through.
	if got := resolveVersion(nil, "v0.1.0"); got != "v0.1.0" {
		t.Errorf("resolveVersion(nil, v0.1.0) = %q, want v0.1.0", got)
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
			h := UpgradeHandler(nil, nil, noopTxRunner(t))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("POST", "/upgrade", strings.NewReader(tc.body)))

			if rec.Code != 400 {
				t.Errorf("status = %d, want 400 for body %q", rec.Code, tc.body)
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
	h := UpgradeHandler(nil, nil, noopTxRunner(t))
	body := strings.NewReader(`{"from": "0000", "to": "0001"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/upgrade", body))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (unknown target with empty fs)", rec.Code)
	}
}

func TestUpgradeHandler_ResolvesSemver(t *testing.T) {
	t.Parallel()

	// Use a tiny sqlFS so Slice can find the target version.
	fsys := fstest.MapFS{
		"sql/0008_a.up.sql": &fstest.MapFile{Data: []byte("")},
	}
	versions := map[string]string{"v0.1.0": "0008"}

	// Upgrade from "" (no current) to "v0.1.0" → resolves to 0008 → Slice returns [0008]
	// → Apply tries to run it. We can't actually execute SQL without a real DB,
	// so this test verifies the *resolution* path by passing an explicit "from"
	// at 0008 too — Slice returns empty → Apply short-circuits → 200 OK.
	h := UpgradeHandler(fsys, versions, noopTxRunner(t))
	body := strings.NewReader(`{"from": "v0.1.0", "to": "v0.1.0"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/upgrade", body))

	if rec.Code != 200 {
		t.Errorf("status = %d, want 200 (no-op upgrade after semver resolution)", rec.Code)
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
		"sql/0001_a.up.sql":   &fstest.MapFile{Data: []byte("")},
		"sql/0001_a.down.sql": &fstest.MapFile{Data: []byte("")},
	}
	h := DowngradeHandler(fsys, nil, noopTxRunner(t))

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
