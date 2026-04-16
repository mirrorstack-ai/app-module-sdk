package main

// CLI flag: --use-events
// Remove this file if the module doesn't consume or emit events.

import (
	"net/http"

	ms "github.com/mirrorstack-ai/app-module-sdk"
)

func init() {
	postInitHooks = append(postInitHooks, registerEvents)
}

func registerEvents() {
	// Declare emitted event names — platform catalog reads from manifest.
	ms.Emits("template.created")

	// Subscribe to another module's event. Platform delivers via Internal-scope POST.
	ms.OnEvent("user.created", func(w http.ResponseWriter, r *http.Request) {
		// Handle the event. Body contains the event payload.
		w.WriteHeader(http.StatusNoContent)
	})
}
