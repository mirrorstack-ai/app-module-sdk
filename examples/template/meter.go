package main

// CLI flag: --use-meter
// Remove this file if the module doesn't emit billable usage events.
//
// Call Record sparingly — once per meaningful action, not per row processed.
// Billing errors are logged but must not fail the calling handler.

import (
	"log"
	"net/http"

	ms "github.com/mirrorstack-ai/app-module-sdk"
	"github.com/go-chi/chi/v5"
)

func init() {
	postInitHooks = append(postInitHooks, registerMeter)
}

func registerMeter() {
	ms.Platform(func(r chi.Router) {
		r.Post("/transcode", func(w http.ResponseWriter, r *http.Request) {
			// ... do the transcode work ...

			if err := ms.Meter(r.Context()).Record("transcode.minutes", 12); err != nil {
				log.Printf("meter: %v", err) // don't fail the handler
			}
			w.WriteHeader(http.StatusOK)
		})
	})
}
