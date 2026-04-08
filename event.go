package mirrorstack

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// eventPathPrefix mounts under /__mirrorstack/ to reserve a platform-owned
// namespace. Mounting at the bare module root would let a developer-defined
// route like m.Public(func(r){ r.Get("/events/list", h) }) collide with
// OnEvent("list"), with chi's duplicate-pattern panic depending on
// registration order. The /__mirrorstack/ prefix is the same convention
// /__mirrorstack/health, /__mirrorstack/platform/* already use.
const eventPathPrefix = "/__mirrorstack/events/"

// OnEvent registers handler for events of the given name from another module.
// Mounts the handler on this module's Internal scope at /events/{name} and
// records the subscription in the manifest's events.subscribes map.
//
// The platform guarantees AT-LEAST-ONCE delivery — handlers must be
// idempotent or implement their own deduplication. Do not rely on
// r.RemoteAddr to identify the sender; in Lambda + API Gateway it is
// always the AGW IP, not the original event source.
//
// Names must not contain path separators (/, \), whitespace, dot-segments
// (..), or null bytes. Call from startup code (init / main), not from
// inside a request handler.
//
// Panics on duplicate registration with the same name.
func (m *Module) OnEvent(name string, handler http.HandlerFunc) {
	path := eventPathPrefix + name
	if !m.registry.AddSubscribe(name, path) {
		panic("mirrorstack: OnEvent(" + name + ") registered twice")
	}
	m.Internal(func(r chi.Router) {
		r.Post(path, handler)
	})
}

// Emits declares that this module emits an event of the given name. The
// declaration appears in the manifest's events.emits list so the platform
// knows the event belongs to this module's vocabulary and other modules
// can subscribe to it. Runtime emission is a separate API (TBD); this
// method is purely a declaration.
//
// Panics on an invalid name (see OnEvent for the rules) or duplicate
// declaration. Call from startup code.
func (m *Module) Emits(name string) {
	if !m.registry.AddEmit(name) {
		panic("mirrorstack: Emits(" + name + ") registered twice")
	}
}

// OnEvent registers an event handler on the default Module created by Init().
// Panics before Init — matches Platform/Public/Internal.
func OnEvent(name string, handler http.HandlerFunc) {
	mustDefault("OnEvent").OnEvent(name, handler)
}

// Emits declares an emitted event on the default Module. Panics before Init.
func Emits(name string) {
	mustDefault("Emits").Emits(name)
}
