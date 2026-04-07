package system

import (
	"io/fs"
	"log"
	"net/http"

	"github.com/mirrorstack-ai/app-module-sdk/internal/httputil"
	"github.com/mirrorstack-ai/app-module-sdk/internal/migration"
	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
)

// ManifestPayload is the JSON shape returned by GET /__mirrorstack/platform/manifest.
// The platform reads this on deploy to discover module identity, capabilities,
// and the current migration version.
type ManifestPayload struct {
	ID        string                            `json:"id"`
	Defaults  ManifestDefaults                  `json:"defaults"`
	Migration string                            `json:"migration"`
	Routes    map[registry.Scope][]registry.Route `json:"routes"`
	Events    ManifestEvents                    `json:"events"`
	Schedules []registry.Schedule               `json:"schedules"`
}

// ManifestDefaults is the default display name and icon. The platform may
// override these per-app installation.
type ManifestDefaults struct {
	Name string `json:"name"`
	Icon string `json:"icon"`
}

// ManifestEvents declares which events the module emits and which it subscribes to.
type ManifestEvents struct {
	Emits      []string          `json:"emits"`
	Subscribes map[string]string `json:"subscribes"`
}

// ManifestHandler returns an http.HandlerFunc that serves the module manifest.
// The migration version is read from sqlFS at request time so a hot-reloaded
// build picks up new migrations without a restart. sqlFS may be nil — the
// migration field will be empty.
//
// Empty-collection normalization (every scope present, no nil maps/slices) is
// the Registry's responsibility — Routes/Emits/Subscribes/Schedules all return
// non-nil zero values.
func ManifestHandler(id, name, icon string, sqlFS fs.FS, reg *registry.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		version, err := migration.LatestVersion(sqlFS)
		if err != nil {
			// Don't fail the manifest — return empty migration so the platform
			// can still discover the module. Log so an unreadable sql/ dir
			// surfaces in CloudWatch.
			log.Printf("mirrorstack: manifest migration version: %v", err)
		}

		httputil.JSON(w, http.StatusOK, ManifestPayload{
			ID:        id,
			Defaults:  ManifestDefaults{Name: name, Icon: icon},
			Migration: version,
			Routes:    reg.Routes(),
			Events:    ManifestEvents{Emits: reg.Emits(), Subscribes: reg.Subscribes()},
			Schedules: reg.Schedules(),
		})
	}
}
