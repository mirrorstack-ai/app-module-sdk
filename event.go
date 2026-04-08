package mirrorstack

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

const eventPathPrefix = "/events/"

// OnEvent registers handler for events of the given name from another module.
// Mounts the handler on this module's Internal scope at /events/{name} and
// records the subscription in the manifest's events.subscribes map.
//
// Event names follow the "module.action" convention (e.g., "oauth.user_deleted").
// Names are validated — see validateRegistrationName for the rules.
//
// Call from startup code (init / main), NOT from inside a request handler.
// The registry is append-only and the auth middleware closure is captured at
// registration time.
func (m *Module) OnEvent(name string, handler http.HandlerFunc) {
	validateRegistrationName("OnEvent", name)
	if m.registry.HasSubscribe(name) {
		panic("mirrorstack: OnEvent(" + name + ") registered twice")
	}
	path := eventPathPrefix + name
	m.Internal(func(r chi.Router) {
		r.Post(path, handler)
	})
	m.registry.AddSubscribe(name, path)
}

// Emit declares that this module emits an event of the given name. The
// declaration appears in the manifest's events.emits list so the platform
// knows the event belongs to this module's vocabulary and other modules can
// subscribe to it. Runtime emission is a separate API (TBD).
//
// Call from startup code. Panics on empty name or duplicate declaration.
func (m *Module) Emit(name string) {
	validateRegistrationName("Emit", name)
	if m.registry.HasEmit(name) {
		panic("mirrorstack: Emit(" + name + ") registered twice")
	}
	m.registry.AddEmit(name)
}

// OnEvent registers an event handler on the default Module created by Init().
// Panics before Init — matches Platform/Public/Internal.
func OnEvent(name string, handler http.HandlerFunc) {
	mustDefault("OnEvent").OnEvent(name, handler)
}

// Emit declares an emitted event on the default Module. Panics before Init.
func Emit(name string) {
	mustDefault("Emit").Emit(name)
}

// validateRegistrationName rejects names that are empty or that would
// produce an unsafe URL path when appended to /events/ or /crons/. Specifically:
// path separators (/, \), dot-segments (..), and whitespace are blocked.
//
// SECURITY: without this check, OnEvent("../admin", h) would let chi
// normalize the registered pattern to "/admin", silently escaping the
// /events/ namespace. The manifest payload would still carry the un-cleaned
// path "/events/../admin" — making the SDK and platform's view of the
// route disagree, and the platform-fired events would land in the wrong
// handler. The same applies to Cron and Emit (Emit doesn't mount a route
// but the name still appears in the manifest payload as a developer-facing
// identifier).
//
// The check is intentionally a deny-list, not a strict allow-list, so that
// reasonable conventions like "media-uploaded" or "Module.Action" remain
// valid. If stricter validation becomes necessary, tighten this in one place.
func validateRegistrationName(kind, name string) {
	if name == "" {
		panic("mirrorstack: " + kind + "() name cannot be empty")
	}
	if strings.ContainsAny(name, "/\\ \t\n\r") {
		panic("mirrorstack: " + kind + "(" + name + ") contains a path separator or whitespace")
	}
	if strings.Contains(name, "..") {
		panic("mirrorstack: " + kind + "(" + name + ") contains '..'")
	}
}
