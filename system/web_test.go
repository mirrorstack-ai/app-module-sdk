package system

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestWebHandler_ServesFileWithCORSAndContentType(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "index.js", "export const x = 1;")
	h := WebHandler(dir)

	req := httptest.NewRequest(http.MethodGet, "/__mirrorstack/web/index.js", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	if got := rec.Body.String(); got != "export const x = 1;" {
		t.Errorf("body = %q, want module body", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("CORS-Allow-Origin = %q, want '*'", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/javascript; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/javascript", got)
	}
}

func TestWebHandler_EmptyWebDir_Always404(t *testing.T) {
	h := WebHandler("")
	req := httptest.NewRequest(http.MethodGet, "/__mirrorstack/web/index.js", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when WebDir unset", rec.Code)
	}
}

func TestWebHandler_PathTraversal_Blocked(t *testing.T) {
	// Secret file lives in the parent of the served dir — a successful
	// traversal would expose it through the SDK's public scope.
	parent := t.TempDir()
	writeFile(t, parent, "secret.txt", "should-not-be-readable")
	dir := filepath.Join(parent, "dist")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, dir, "index.js", "ok")

	h := WebHandler(dir)

	cases := []string{
		"/__mirrorstack/web/../secret.txt",
		"/__mirrorstack/web/..%2Fsecret.txt",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, p, nil)
			rec := httptest.NewRecorder()
			h(rec, req)
			if rec.Code == http.StatusOK {
				t.Fatalf("traversal succeeded for %q: body=%s", p, rec.Body)
			}
			if got := rec.Body.String(); got == "should-not-be-readable" {
				t.Errorf("secret leaked for %q", p)
			}
		})
	}
}

func TestWebHandler_SymlinkEscape_Blocked(t *testing.T) {
	parent := t.TempDir()
	writeFile(t, parent, "secret.txt", "should-not-be-readable")
	dir := filepath.Join(parent, "dist")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink("../secret.txt", filepath.Join(dir, "leak.js")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	h := WebHandler(dir)
	req := httptest.NewRequest(http.MethodGet, "/__mirrorstack/web/leak.js", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("symlink escape succeeded: body=%s", rec.Body)
	}
	if got := rec.Body.String(); got == "should-not-be-readable" {
		t.Error("secret leaked through symlink")
	}
}

func TestWebHandler_MissingFile_Returns404(t *testing.T) {
	dir := t.TempDir()
	h := WebHandler(dir)
	req := httptest.NewRequest(http.MethodGet, "/__mirrorstack/web/missing.js", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestWebHandler_Directory_Returns404(t *testing.T) {
	// Listing the dir would expose file structure; the handler should
	// only serve actual files.
	parent := t.TempDir()
	if err := os.Mkdir(filepath.Join(parent, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	h := WebHandler(parent)
	req := httptest.NewRequest(http.MethodGet, "/__mirrorstack/web/nested", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for directory request", rec.Code)
	}
}

func TestWebHandler_Options_PreflightSucceeds(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "index.js", "ok")
	h := WebHandler(dir)

	req := httptest.NewRequest(http.MethodOptions, "/__mirrorstack/web/index.js", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204 for OPTIONS preflight", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Errorf("missing Allow-Methods on preflight")
	}
}

// Under `dev --tunnel` the page is a public https origin while the bundle is
// served from http://localhost, so Chrome sends a local-network preflight and
// blocks the load unless this handler opts in. The block happens in the
// browser, so a regression here shows up only as a blank settings page with
// nothing in the module's log — hence covering both spellings explicitly.
func TestWebHandler_LocalNetworkPreflight_OptsIn(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "index.js", "ok")
	h := WebHandler(dir)

	for _, tt := range []struct{ name, reqHeader, wantHeader string }{
		{"chrome 138+", "Access-Control-Request-Local-Network-Access", "Access-Control-Allow-Local-Network-Access"},
		{"chrome <138", "Access-Control-Request-Private-Network", "Access-Control-Allow-Private-Network"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodOptions, "/__mirrorstack/web/index.js", nil)
			req.Header.Set("Origin", "https://apps.mirrorstack.ai")
			req.Header.Set(tt.reqHeader, "true")
			rec := httptest.NewRecorder()
			h(rec, req)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204", rec.Code)
			}
			if got := rec.Header().Get(tt.wantHeader); got != "true" {
				t.Errorf("%s = %q, want \"true\" — the browser drops the bundle without it", tt.wantHeader, got)
			}
		})
	}
}

// The opt-in is echoed only when asked for, so an ordinary fetch does not
// advertise local-network access it was never requested to grant.
func TestWebHandler_NoLocalNetworkRequest_NoOptIn(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "index.js", "ok")
	h := WebHandler(dir)

	req := httptest.NewRequest(http.MethodOptions, "/__mirrorstack/web/index.js", nil)
	req.Header.Set("Origin", "https://apps.mirrorstack.ai")
	rec := httptest.NewRecorder()
	h(rec, req)
	for _, header := range []string{"Access-Control-Allow-Local-Network-Access", "Access-Control-Allow-Private-Network"} {
		if got := rec.Header().Get(header); got != "" {
			t.Errorf("%s = %q on an unrequested preflight, want empty", header, got)
		}
	}
}

func TestWebHandler_BadMethod_405(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "index.js", "ok")
	h := WebHandler(dir)
	req := httptest.NewRequest(http.MethodPost, "/__mirrorstack/web/index.js", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405 for POST", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got == "" {
		t.Errorf("missing Allow header on 405")
	}
}

func TestContentTypeFor(t *testing.T) {
	cases := map[string]string{
		"x.js":    "application/javascript; charset=utf-8",
		"x.mjs":   "application/javascript; charset=utf-8",
		"x.map":   "application/json",
		"x.css":   "text/css; charset=utf-8",
		"x.html":  "text/html; charset=utf-8",
		"x.other": "application/octet-stream",
	}
	for path, want := range cases {
		if got := contentTypeFor(path); got != want {
			t.Errorf("contentTypeFor(%q) = %q, want %q", path, got, want)
		}
	}
}
