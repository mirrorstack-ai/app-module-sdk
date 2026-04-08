package httputil

import "net/http"

// MaxBytes wraps the request body with http.MaxBytesReader, capping reads at n bytes.
// Requests exceeding the limit will receive 413 Request Entity Too Large.
func MaxBytes(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}
