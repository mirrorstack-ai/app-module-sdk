package main

// CLI flag: --use-storage
// Remove this file if the module doesn't upload/download files.

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	ms "github.com/mirrorstack-ai/app-module-sdk"
)

func init() {
	postInitHooks = append(postInitHooks, registerStorage)
}

func registerStorage() {
	ms.Platform(func(r chi.Router) {
		r.Post("/uploads/init", initUpload)
	})
}

func initUpload(w http.ResponseWriter, r *http.Request) {
	_, err := ms.Storage(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Use storer to issue a presigned multipart upload URL, list objects, etc.
	w.WriteHeader(http.StatusNotImplemented)
}
