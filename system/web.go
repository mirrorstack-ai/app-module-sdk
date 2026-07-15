package system

import (
	"io/fs"
	"net/http"
	"os"
	pathpkg "path"
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
// Path traversal is blocked by os.Root, which rejects paths and symlinks that
// escape rootDir while opening the file.
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
		if !fs.ValidPath(rel) {
			http.NotFound(w, r)
			return
		}

		root, err := os.OpenRoot(abs)
		if err != nil {
			if os.IsNotExist(err) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, "io error", http.StatusInternalServerError)
			return
		}
		defer root.Close()

		file, err := root.Open(rel)
		if err != nil {
			// Do not reveal whether a path is missing, inaccessible, or
			// rejected because it escapes the configured web root.
			http.NotFound(w, r)
			return
		}
		defer file.Close()

		fi, err := file.Stat()
		if err != nil || fi.IsDir() {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", contentTypeFor(rel))
		http.ServeContent(w, r, pathpkg.Base(rel), fi.ModTime(), file)
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
	switch pathpkg.Ext(path) {
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
