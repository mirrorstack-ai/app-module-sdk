package system

import (
	"io/fs"
	"log"
	"net/http"

	"github.com/mirrorstack-ai/app-module-sdk/i18n"
	"github.com/mirrorstack-ai/app-module-sdk/internal/contributions"
	"github.com/mirrorstack-ai/app-module-sdk/internal/httputil"
	"github.com/mirrorstack-ai/app-module-sdk/internal/migration"
	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
)

// ManifestPayload is the JSON shape returned by GET /__mirrorstack/platform/manifest.
// The platform reads this on deploy to discover module identity, capabilities,
// migration versions, and the semver→migration mapping it needs to translate
// lifecycle calls.
type ManifestPayload struct {
	ID string `json:"id"`
	// Slug is the catalog handle (e.g. "oauth"). Empty for dev/legacy
	// modules that haven't been assigned a slug yet — the platform falls
	// back to ID-based addressing in that case.
	Slug         string                              `json:"slug,omitempty"`
	Defaults     ManifestDefaults                    `json:"defaults"`
	Description  string                              `json:"description,omitempty"`
	Dependencies []registry.Dependency               `json:"dependencies"`
	Migration    MigrationVersions                   `json:"migration"`
	Versions     map[string]MigrationVersions        `json:"versions"`
	Routes       map[registry.Scope][]registry.Route `json:"routes"`
	Events       ManifestEvents                      `json:"events"`
	Schedules    []registry.Schedule                 `json:"schedules"`
	Tasks        []registry.Task                     `json:"tasks"`
	Permissions  []registry.Permission               `json:"permissions"`
	// Metrics lists the usage metrics this module declares (ms.Meter). The
	// platform populates its metric_definitions catalog (kind/unit/price) from
	// this at install/publish, so the catalog is authoritative before any usage
	// event arrives. Omitted when the module declares no metrics.
	Metrics []registry.MetricDecl `json:"metrics,omitempty"`
	MCP     ManifestMCP           `json:"mcp"`
	// UI is the module's declared UI surface (RegisterUI). Nil/absent when
	// the module ships no UI — callers must nil-check before reading.
	UI *registry.ModuleUI `json:"ui,omitempty"`
	// Provides lists the extension slots this module declares for
	// others to contribute to (ms.Provide). The catalog reads this to know
	// what other modules can plug into. Always present; empty array
	// when no slots are declared.
	Provides []contributions.SlotInfo `json:"provides"`
	// ContributesTo lists the host slots this module pushes INTO
	// (ms.ContributesTo) — the contributor side. The catalog (CLI in dev)
	// validates each against the host's provides and performs
	// the registration after app-owner approval. Always present; empty
	// array when the module contributes nothing.
	ContributesTo []registry.OutboundContribution `json:"contributesTo"`
}

// ManifestMCP declares the MCP tool and resource surface of the module. The
// platform catalog ingests this at publish time so an aggregated MCP server
// can route agent tool calls without live-listing per module. Handlers are
// stripped — only name, description, and schemas appear on the wire.
type ManifestMCP struct {
	Tools     []MCPToolEntry     `json:"tools"`
	Resources []MCPResourceEntry `json:"resources"`
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

// ManifestDefaults is the default display name, icon, and tags. The platform
// may override name/icon per-app installation; tags are module-level badges.
//
// NameLabels carries per-locale display names (resolved from the module's
// i18n catalog key "module.name"); empty when the module declared none, in
// which case the platform falls back to Name.
type ManifestDefaults struct {
	Name       string            `json:"name"`
	Icon       string            `json:"icon"`
	Tags       []string          `json:"tags,omitempty"`
	NameLabels map[string]string `json:"nameLabels,omitempty"`
}

// ManifestEvents declares which events the module emits and which it subscribes to.
type ManifestEvents struct {
	Emits      []string          `json:"emits"`
	Subscribes map[string]string `json:"subscribes"`
}

// buildManifestMCP projects the registry's MCP declarations into wire-safe
// entries (Handler stripped). Uses the shared toolEntries/resourceEntries
// helpers from mcp.go so list endpoints and manifest stay in lockstep.
func buildManifestMCP(reg *registry.Registry) ManifestMCP {
	return ManifestMCP{
		Tools:     toolEntries(reg.MCPTools()),
		Resources: resourceEntries(reg.MCPResources()),
	}
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
//
// contribReg is the module's contribution-slot registry. Pass nil to omit
// declared contributions from the manifest entirely (e.g. tests).
func ManifestHandler(id, slug, name, icon string, tags []string, sqlFS fs.FS, versions map[string]MigrationVersions, reg *registry.Registry, contribReg *contributions.Registry) http.HandlerFunc {
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

		var contribSlots []contributions.SlotInfo
		if contribReg != nil {
			contribSlots = contribReg.List()
		} else {
			contribSlots = []contributions.SlotInfo{}
		}

		httputil.JSON(w, http.StatusOK, ManifestPayload{
			ID:            id,
			Slug:          slug,
			Defaults:      ManifestDefaults{Name: name, Icon: icon, Tags: tags, NameLabels: i18n.Lookup("module.name")},
			Description:   reg.Description(),
			Dependencies:  reg.Dependencies(),
			Migration:     MigrationVersions{App: appVersion, Module: moduleVersion},
			Versions:      versions,
			Routes:        reg.Routes(),
			Events:        ManifestEvents{Emits: reg.Emits(), Subscribes: reg.Subscribes()},
			Schedules:     reg.Schedules(),
			Tasks:         reg.Tasks(),
			Permissions:   reg.Permissions(),
			Metrics:       reg.Metrics(),
			MCP:           buildManifestMCP(reg),
			UI:            reg.UI(),
			Provides:      contribSlots,
			ContributesTo: reg.OutboundContributions(),
		})
	}
}
