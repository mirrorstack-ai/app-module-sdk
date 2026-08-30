package httpx

import "net/http"

// NoStore prevents browsers and intermediaries from retaining a response.
// Mount it on authenticated or otherwise request-specific route groups.
func NoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		next.ServeHTTP(w, r)
	})
}
