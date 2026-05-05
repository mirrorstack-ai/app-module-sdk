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
	// Co-locate an OPTIONAL dependency declaration via ms.OptionalDependOn —
	// catalog treats the dep as optional, and the module still installs if
	// "user" isn't present (the event source just doesn't exist, so the
	// handler never fires).
	ms.OnEvent("user.created", func(w http.ResponseWriter, r *http.Request) {
		// Handle the event. Body contains the event payload.
		// Inside, use ms.Resolve[T]("user") to call into the user module if present.
		w.WriteHeader(http.StatusNoContent)
	}, ms.OptionalDependOn("user@^1"))
}
