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
	// Wrap the handler with ms.Needs(id, ...) to declare an OPTIONAL dependency
	// on the module that emits the event — catalog treats it as optional and
	// the module still installs if "user" isn't present.
	ms.OnEvent("user.created", ms.Needs("user@^1", func(w http.ResponseWriter, r *http.Request) {
		// Handle the event. Body contains the event payload.
		// Inside, use ms.Resolve[T]("user") to call into the user module if present.
		w.WriteHeader(http.StatusNoContent)
	}))
}
