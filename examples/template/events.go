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
	// @anna/user isn't present (the event source just doesn't exist, so the
	// handler never fires).
	//
	// Inside the OptionalDependOn callback, n.Table(...) declares which
	// relations from @anna/user's mod_<id> schema this handler reads, and
	// n.Event(...) declares which additional events it consumes. The catalog
	// validates each name at install time and surfaces them for app-owner
	// approval; on consent, GRANT SELECT is emitted per Table.
	ms.OnEvent("user.created", func(w http.ResponseWriter, r *http.Request) {
		// Handle the event. Body contains the event payload.
		// Inside, use ms.Resolve[T]("@anna/user") to call into the user module if present.
		w.WriteHeader(http.StatusNoContent)
	}, ms.OptionalDependOn("@anna/user@^1.0.0", func(n *ms.Need) {
		n.Table("user_profiles")    // SELECT against @anna/user.user_profiles
		n.Event("user.deactivated") // additional event subscription
	}))
}
