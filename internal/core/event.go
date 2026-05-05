package core

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
)

// eventPathPrefix mounts under the reserved /__mirrorstack/ namespace so
// developer-defined routes (registered via m.Public/Platform/Internal) cannot
// collide with auto-generated event handlers. Same convention as
// /__mirrorstack/health and /__mirrorstack/platform/*.
const eventPathPrefix = "/__mirrorstack/events/"

// OnEventOption configures an OnEvent registration. Today its only
// producer is ms.OptionalDependOn, which co-locates an optional
// cross-module dep declaration with the event handler that uses it.
type OnEventOption func(*onEventOptions)

// onEventOptions accumulates state from the variadic options applied to
// an OnEvent call. Kept unexported so the option-application is fully
// owned by this package.
type onEventOptions struct {
	optionalDeps []registry.Dependency
}

// OnEvent registers handler for events of the given name from another
// module. Mounts on this module's Internal scope at
// /__mirrorstack/events/{name} and records the subscription in the
// manifest's events.subscribes map.
//
// Optional dependency declarations co-locate via the variadic opts:
//
//	ms.OnEvent("@anna/billing/payment", onPayment,
//	    ms.OptionalDependOn("@anna/billing@^1", func(n *ms.Need) {
//	        n.Table("invoices")
//	    }))
//
// Each OptionalDependOn-produced option appends an optional dep to the
// manifest. The dep is "scoped" to this handler in spirit — if the dep
// isn't installed, the event never fires and the handler never runs, so
// missing-dep failures are harmless.
//
// The platform guarantees AT-LEAST-ONCE delivery — handlers must be
// idempotent or implement their own deduplication.
//
// Names must not contain path separators (/, \), whitespace, dot-segments
// (..), or null bytes. Call from startup code (init / main), not from
// inside a request handler. Panics on duplicate registration.
func (m *Module) OnEvent(name string, handler http.HandlerFunc, opts ...OnEventOption) {
	o := &onEventOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}
	for _, dep := range o.optionalDeps {
		m.registry.AddDependency(dep)
	}
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
func OnEvent(name string, handler http.HandlerFunc, opts ...OnEventOption) {
	mustDefault("OnEvent").OnEvent(name, handler, opts...)
}

// Emits declares an emitted event on the default Module. Panics before Init.
func Emits(name string) {
	mustDefault("Emits").Emits(name)
}
