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
// migration version, and the semver→migration mapping it needs to translate
// lifecycle calls.
type ManifestPayload struct {
	ID        string           `json:"id"`
	Defaults  ManifestDefaults `json:"defaults"`
	Migration string           `json:"migration"`
	// Versions declares the mapping from human release tags (e.g., "v0.1.0")
	// to migration numbers (e.g., "0008"). The platform reads this on deploy
	// and uses it to translate semver release requests into the numeric
	// migration numbers the lifecycle handlers expect. Empty map is valid:
	// modules without formal releases just declare migration numbers directly.
	Versions    map[string]string                   `json:"versions"`
	Routes      map[registry.Scope][]registry.Route `json:"routes"`
	Events      ManifestEvents                      `json:"events"`
	Schedules   []registry.Schedule                 `json:"schedules"`
	Permissions []registry.Permission               `json:"permissions"`
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
// versions is the module's declared semver→migration map. It is exposed
// read-only so the platform can translate semver release tags to the migration
// numbers the lifecycle handlers require. A nil map is normalized to an empty
// object so the JSON output is always `"versions":{}` instead of
// `"versions":null` — the handler owns the output contract and normalizes
// here the same way Registry normalizes Routes/Emits/Subscribes/Schedules at
// their getters, so every manifest field is a non-nil zero value.
func ManifestHandler(id, name, icon string, sqlFS fs.FS, versions map[string]string, reg *registry.Registry) http.HandlerFunc {
	if versions == nil {
		versions = map[string]string{}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		version, err := migration.LatestVersion(sqlFS, migration.ScopeApp)
		if err != nil {
			// Don't fail the manifest — return empty migration so the platform
			// can still discover the module. Log a sanitized message: in dev
			// mode with os.DirFS the wrapped error would include the resolved
			// filesystem path, which is dev-environment noise we don't want
			// in CloudWatch. The operator can re-check Config.SQL locally.
			log.Printf("mirrorstack: manifest migration version unavailable (check Config.SQL is set correctly)")
		}

		httputil.JSON(w, http.StatusOK, ManifestPayload{
			ID:          id,
			Defaults:    ManifestDefaults{Name: name, Icon: icon},
			Migration:   version,
			Versions:    versions,
			Routes:      reg.Routes(),
			Events:      ManifestEvents{Emits: reg.Emits(), Subscribes: reg.Subscribes()},
			Schedules:   reg.Schedules(),
			Permissions: reg.Permissions(),
		})
	}
}
