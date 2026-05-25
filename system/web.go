package system

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// WebHandler serves the module's React bundle output (the directory the
// module author named in Config.WebDir, typically "web/dist") under
// /__mirrorstack/web/*. The platform's catch-all module page fetches the
// bundle from here and dynamically imports the named exports declared in
// ms.RegisterUI(DefaultPages).
//
// CORS is intentionally permissive: bundles are public static assets,
// browser-fetched from a different origin (web-applications on :3001 vs.
// the module on :8080 in local dev), and carry no credentials. The
// /__mirrorstack/web/* namespace is read-only — no cookie/auth flows.
//
// If rootDir is empty, every request 404s — the module declared no web
// surface.
//
// Path traversal is blocked by validating the cleaned filesystem path is
// rooted under rootDir; symlinks that escape the dir are rejected.
func WebHandler(rootDir string) http.HandlerFunc {
	if rootDir == "" {
		return func(w http.ResponseWriter, _ *http.Request) {
			http.NotFound(w, nil)
		}
	}
	abs, err := filepath.Abs(rootDir)
	if err != nil {
		// Bad config: still install a handler that 404s rather than
		// panicking at module startup. The module author sees the
		// problem the first time the platform tries to fetch.
		return func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "web dir invalid", http.StatusInternalServerError)
		}
	}
	// Resolve symlinks at construction so per-request EvalSymlinks
	// results can be prefix-compared. Without this, macOS's tempdir
	// (/var → /private/var symlink) breaks the prefix check for every
	// served file. If the dir doesn't exist yet, fall back to abs — the
	// handler will 404 anyway when files aren't found.
	root := abs
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		root = resolved
	}
	return func(w http.ResponseWriter, r *http.Request) {
		setCORS(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD, OPTIONS")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// chi captured the rest-of-path under "*"; we recover it from
		// URL.Path so this handler can also be unit-tested without chi.
		rel := strings.TrimPrefix(r.URL.Path, "/__mirrorstack/web/")
		if rel == "" || rel == "." {
			http.NotFound(w, r)
			return
		}
		full := filepath.Join(root, rel)
		// Defense-in-depth: refuse anything that escapes rootDir even
		// after filepath.Clean (e.g. via symlink). EvalSymlinks resolves
		// links so we compare the real on-disk path.
		real, err := filepath.EvalSymlinks(full)
		if err != nil {
			if os.IsNotExist(err) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, "io error", http.StatusInternalServerError)
			return
		}
		if !strings.HasPrefix(real, root+string(filepath.Separator)) && real != root {
			http.NotFound(w, r) // pretend it doesn't exist rather than confirm escape attempt
			return
		}
		fi, err := os.Stat(real)
		if err != nil || fi.IsDir() {
			http.NotFound(w, r)
			return
		}

		// Material content-type for the two file kinds we ship today —
		// the JS bundle and its sourcemap. http.ServeFile would also work
		// but adds Last-Modified / ETag handling we don't need yet.
		w.Header().Set("Content-Type", contentTypeFor(real))
		http.ServeFile(w, r, real)
	}
}

func setCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	// Bundles are immutable per build; the platform replaces them via a
	// fresh fetch when the module restarts. Short max-age keeps the dev
	// loop responsive while still letting browsers reuse within a render.
	w.Header().Set("Cache-Control", "no-cache")
}

func contentTypeFor(path string) string {
	switch filepath.Ext(path) {
	case ".js", ".mjs":
		return "application/javascript; charset=utf-8"
	case ".map":
		return "application/json"
	case ".css":
		return "text/css; charset=utf-8"
	case ".html":
		return "text/html; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}
