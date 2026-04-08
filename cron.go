package mirrorstack

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

const cronPathPrefix = "/crons/"

// Cron registers a handler to run on a cron schedule. The handler is mounted
// on this module's Internal scope at /crons/{name}. The schedule is recorded
// in the manifest so the platform's scheduler knows what URL to POST to when
// the cron fires.
//
// The cron string is passed through to the platform unchanged — validation
// happens platform-side so module developers see one error format regardless
// of the scheduler implementation.
//
// Call from startup code. Panics on empty schedule, an invalid name (see
// validateRegistrationName), or duplicate name registration.
//
//	mod.Cron("cleanup-temp", "0 3 * * *", cleanupHandler)
func (m *Module) Cron(name, cron string, handler http.HandlerFunc) {
	validateRegistrationName("Cron", name)
	if cron == "" {
		panic("mirrorstack: Cron(" + name + ") schedule cannot be empty")
	}
	if m.registry.HasSchedule(name) {
		panic("mirrorstack: Cron(" + name + ") registered twice")
	}
	path := cronPathPrefix + name
	m.Internal(func(r chi.Router) {
		r.Post(path, handler)
	})
	m.registry.AddSchedule(name, cron, path)
}

// Cron registers a cron job on the default Module created by Init(). Panics
// before Init — matches Platform/Public/Internal.
func Cron(name, cron string, handler http.HandlerFunc) {
	mustDefault("Cron").Cron(name, cron, handler)
}
