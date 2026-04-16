package main

// CLI flag: --use-cache
// Remove this file if the module doesn't need per-app Redis cache.

import (
	"net/http"

	ms "github.com/mirrorstack-ai/app-module-sdk"
	"github.com/go-chi/chi/v5"
)

func init() {
	postInitHooks = append(postInitHooks, registerCache)
}

func registerCache() {
	ms.Platform(func(r chi.Router) {
		r.Get("/cache/ping", func(w http.ResponseWriter, r *http.Request) {
			c, release, err := ms.Cache(r.Context())
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			defer release()
			_ = c // use c.Set/Get/Delete here
			w.WriteHeader(http.StatusOK)
		})
	})
}
