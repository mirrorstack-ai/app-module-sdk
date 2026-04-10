package mirrorstack

import (
	"net/http"

	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
)

// cronPathPrefix mounts under the reserved /__mirrorstack/ namespace.
// See eventPathPrefix in event.go for the collision-prevention rationale.
const cronPathPrefix = "/__mirrorstack/crons/"

// Cron registers a handler to run on a cron schedule. The handler is mounted
// on this module's Internal scope at /__mirrorstack/crons/{name}. The
// schedule is recorded in the manifest so the platform's scheduler knows
// what URL to POST when the cron fires.
//
// The schedule string is passed through to the platform unchanged —
// validation happens platform-side so module developers see one error format
// regardless of the scheduler implementation.
//
// Names must not contain path separators (/, \), whitespace, dot-segments
// (..), or null bytes. Do not rely on r.RemoteAddr to identify the
// scheduler; in Lambda + API Gateway it is always the AGW IP. Call from
// startup code, not a request handler.
//
// Panics on empty schedule, an invalid name, or duplicate name registration.
//
//	mod.Cron("cleanup-temp", "0 3 * * *", cleanupHandler)
func (m *Module) Cron(name, schedule string, handler http.HandlerFunc) {
	if schedule == "" {
		panic("mirrorstack: Cron(" + name + ") schedule cannot be empty")
	}
	path := cronPathPrefix + name
	if !m.registry.AddSchedule(name, schedule, path) {
		panic("mirrorstack: Cron(" + name + ") registered twice")
	}
	m.scopedSingleRoute(registry.ScopeInternal, m.internalAuth, "POST", path, handler)
}

// Cron registers a cron job on the default Module created by Init(). Panics
// before Init — matches Platform/Public/Internal.
func Cron(name, schedule string, handler http.HandlerFunc) {
	mustDefault("Cron").Cron(name, schedule, handler)
}
