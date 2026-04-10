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
// migration versions, and the semver→migration mapping it needs to translate
// lifecycle calls.
type ManifestPayload struct {
	ID          string                              `json:"id"`
	Defaults    ManifestDefaults                    `json:"defaults"`
	Migration   MigrationVersions                   `json:"migration"`
	Versions    map[string]MigrationVersions        `json:"versions"`
	Routes      map[registry.Scope][]registry.Route `json:"routes"`
	Events      ManifestEvents                      `json:"events"`
	Schedules   []registry.Schedule                 `json:"schedules"`
	Tasks       []registry.Task                     `json:"tasks"`
	Permissions []registry.Permission               `json:"permissions"`
}

// MigrationVersions is the per-scope migration number set. Used both for the
// current bundled version (ManifestPayload.Migration) and for each entry in
// the semver→migration map (ManifestPayload.Versions).
//
// Module is omitempty so modules that don't use the cross-app shared schema
// (the vast majority) don't see the field in the wire shape. App is always
// present so consumers can rely on its existence.
type MigrationVersions struct {
	App    string `json:"app"`
	Module string `json:"module,omitempty"`
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
// The migration versions are read from sqlFS at request time so a hot-reloaded
// build picks up new migrations without a restart. sqlFS may be nil — both
// migration fields will be empty.
//
// versions is the module's declared semver→migration map. A nil map is
// normalized to an empty object so the JSON output is always `"versions":{}`
// instead of `"versions":null` — the handler owns the output contract and
// normalizes here the same way Registry normalizes Routes/Emits/Subscribes/
// Schedules at their getters.
func ManifestHandler(id, name, icon string, sqlFS fs.FS, versions map[string]MigrationVersions, reg *registry.Registry) http.HandlerFunc {
	if versions == nil {
		versions = map[string]MigrationVersions{}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		// Read each scope independently. Errors are logged with a sanitized
		// message — in dev mode with os.DirFS the wrapped error would include
		// the resolved filesystem path, which is dev-environment noise we
		// don't want in CloudWatch. The operator can re-check Config.SQL
		// locally. The manifest still serves successfully with the empty
		// version so the platform can discover the module.
		appVersion, appErr := migration.LatestVersion(sqlFS, migration.ScopeApp)
		if appErr != nil {
			log.Printf("mirrorstack: manifest app migration version unavailable (check Config.SQL is set correctly)")
		}
		moduleVersion, moduleErr := migration.LatestVersion(sqlFS, migration.ScopeModule)
		if moduleErr != nil {
			log.Printf("mirrorstack: manifest module migration version unavailable (check Config.SQL is set correctly)")
		}

		httputil.JSON(w, http.StatusOK, ManifestPayload{
			ID:          id,
			Defaults:    ManifestDefaults{Name: name, Icon: icon},
			Migration:   MigrationVersions{App: appVersion, Module: moduleVersion},
			Versions:    versions,
			Routes:      reg.Routes(),
			Events:      ManifestEvents{Emits: reg.Emits(), Subscribes: reg.Subscribes()},
			Schedules:   reg.Schedules(),
			Tasks:       reg.Tasks(),
			Permissions: reg.Permissions(),
		})
	}
}
