package system

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"

	"github.com/mirrorstack-ai/app-module-sdk/i18n"
	"github.com/mirrorstack-ai/app-module-sdk/internal/contributions"
	"github.com/mirrorstack-ai/app-module-sdk/internal/httputil"
	"github.com/mirrorstack-ai/app-module-sdk/internal/migration"
	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
)

// manifestHashHeader carries hex(sha256(exact served manifest body bytes)).
// The platform and CLI READ this header rather than recomputing the hash, so
// they can never disagree with the module about its declared surface. The hash
// covers the Go-declared manifest (id/slug/pages/permissions/schedules/
// migrations/...) and is stable across esbuild JS rebuilds, because the
// manifest body carries no JS.
const manifestHashHeader = "X-MS-Manifest-Hash"

// ManifestPayload is the JSON shape returned by GET /__mirrorstack/platform/manifest.
// The platform reads this on deploy to discover module identity, capabilities,
// migration versions, and the semver→migration mapping it needs to translate
// lifecycle calls.
type ManifestPayload struct {
	ID string `json:"id"`
	// Slug is the catalog handle (e.g. "oauth"). Empty for dev/legacy
	// modules that haven't been assigned a slug yet — the platform falls
	// back to ID-based addressing in that case.
	Slug        string           `json:"slug,omitempty"`
	Defaults    ManifestDefaults `json:"defaults"`
	Description string           `json:"description,omitempty"`
	// DescriptionLabels carries per-locale description display strings (locale →
	// text), resolved from the module's i18n catalog (ms.Config.DescriptionLabel).
	// Omitted when the module declared none, in which case the platform falls
	// back to Description. Mirrors Permission.Descriptions / MetricDecl.Labels.
	DescriptionLabels map[string]string `json:"descriptionLabels,omitempty"`
	// Client declares the module-local source and build-output directories for
	// the optional custom-app client. The CLI consumes these paths during local
	// development; package identity and versioning remain platform-owned.
	Client       *ClientSpec                         `json:"client,omitempty"`
	Dependencies []registry.Dependency               `json:"dependencies"`
	Migration    MigrationVersions                   `json:"migration"`
	Versions     map[string]MigrationVersions        `json:"versions"`
	Routes       map[registry.Scope][]registry.Route `json:"routes"`
	Events       ManifestEvents                      `json:"events"`
	// Exposes lists the tables this module marks readable (SELECT-eligible)
	// by a depending module (ms.ExposeTable). The platform catalog issues
	// GRANT SELECT against the depending module's DB role after the app
	// owner approves the dependency. Always present; tables is an empty
	// array when nothing is exposed.
	Exposes     ManifestExposes       `json:"exposes"`
	Schedules   []registry.Schedule   `json:"schedules"`
	Tasks       []registry.Task       `json:"tasks"`
	Permissions []registry.Permission `json:"permissions"`
	// Resources declares privileged runtime resources the module needs. The
	// platform must not vend a resource merely because the SDK supports it.
	Resources ManifestResources `json:"resources"`
	// Metrics lists the usage metrics this module declares (ms.Meter). The
	// platform populates its metric_definitions catalog (kind/unit/price) from
	// this at install/publish, so the catalog is authoritative before any usage
	// event arrives. Omitted when the module declares no metrics.
	Metrics []registry.MetricDecl `json:"metrics,omitempty"`
	// AbsorbInfra is ms.AbsorbInfra(): pass EVERY platform infra metric through
	// to the app owner at 0. A BOOLEAN, deliberately — the platform expands it
	// against its own catalog, so no list of infra metric names travels in a
	// manifest to go stale. Omitted when the module did not declare it.
	AbsorbInfra bool        `json:"absorbInfra,omitempty"`
	MCP         ManifestMCP `json:"mcp"`
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

// ClientSpec identifies the module-local client project and its compiled
// output. Both paths are canonical, slash-separated paths relative to their
// containing project: Dir is relative to the module root and OutputDir is
// relative to Dir. Module construction validates this contract.
type ClientSpec struct {
	Dir       string `json:"dir"`
	OutputDir string `json:"outputDir"`
}

// ManifestResources is the module's opt-in runtime-resource contract. Empty is
// encoded as {} so old modules remain explicitly resource-free.
type ManifestResources struct {
	Storage bool `json:"storage,omitempty"`
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
//
// TagLabels carries per-locale localized tag LISTS (locale → []tag), the list
// counterpart of NameLabels. It is resolved from the module's i18n catalog key
// "module.tags", whose per-locale value packs the tag list into a single
// comma-separated string (e.g. en-US "Auth, Payments", zh-TW "驗證, 付款"); the
// manifest splits on "," and space-trims each tag. Empty when the module
// declared no "module.tags" key, in which case the platform falls back to the
// raw Tags list. Tags stays the default/fallback exactly as Name does.
type ManifestDefaults struct {
	Name       string              `json:"name"`
	Icon       string              `json:"icon"`
	Tags       []string            `json:"tags,omitempty"`
	NameLabels map[string]string   `json:"nameLabels,omitempty"`
	TagLabels  map[string][]string `json:"tagLabels,omitempty"`
}

// tagCatalogKey / tagLabelSep define the "module.tags" authoring convention:
// one catalog key per locale holding a comma-separated tag list (see
// ManifestDefaults.TagLabels). Kept next to the handler that reads them so the
// wire convention lives in one place.
const (
	tagCatalogKey = "module.tags"
	tagLabelSep   = ","
)

const (
	uiPageCatalogPrefix  = "ui.pages"
	uiPageMainSurfaceKey = "main"
)

func uiPageCatalogKey(surface, route string) string {
	if surface == registry.UISurfaceMain {
		surface = uiPageMainSurfaceKey
	}
	return uiPageCatalogPrefix + "." + surface + "." + route
}

// localizeUIPages fills UIPage title/description label maps from catalog keys
// "ui.pages.<surface>.<route>.title" and ".description". A page is already
// identified by its (Surface, Route) pair in validateUI, so the catalog uses
// that same pair rather than introducing a separate slug. The route is one
// verbatim JSON key: authors have no transformation rule to remember, and two
// distinct routes cannot collide. The empty main surface is written as "main":
//
//	{"ui":{"pages":{
//	  "main":     {"/": {"title":"影片"}, "/sessions": {"title":"工作階段"}},
//	  "settings": {"/": {"title":"影片核心", "description":"設定影片分類與播放預設值。"}}
//	}}}
func localizeUIPages(ui *registry.ModuleUI) *registry.ModuleUI {
	if ui == nil {
		return nil
	}

	// Registry.UI already returns a deep copy, so the manifest can safely
	// enrich these pages in place without cloning them again.
	for i := range ui.DefaultPages {
		p := &ui.DefaultPages[i]
		base := uiPageCatalogKey(p.Surface, p.Route)

		// A non-empty author-set map is more specific and more local, so it
		// wins over the process-wide catalog.
		if len(p.TitleLabels) == 0 {
			if labels := i18n.Lookup(base + ".title"); len(labels) > 0 {
				p.TitleLabels = labels
			}
		}
		if len(p.DescriptionLabels) == 0 {
			if labels := i18n.Lookup(base + ".description"); len(labels) > 0 {
				p.DescriptionLabels = labels
			}
		}
	}

	// Lookup results are emitted verbatim, including empty values: an empty
	// translation is pending and consumers deliberately fall back with ||.
	return ui
}

// ManifestEvents declares which events the module emits and which it subscribes to.
type ManifestEvents struct {
	Emits      []string          `json:"emits"`
	Subscribes map[string]string `json:"subscribes"`
}

// ManifestExposes declares the read-only (SELECT) surface this module opens to
// depending modules. Tables is a flat, sorted, de-duplicated list of table
// NAMES (ms.ExposeTable) in the module's `mod_<id>` schema. There is no
// per-consumer "readableBy" list: the producer only marks a table
// SELECT-eligible; the app owner decides WHO reads it by approving a
// dependency. v1 is TABLES ONLY.
type ManifestExposes struct {
	Tables []string `json:"tables"`
}

// ManifestDocument is one canonical manifest build. Body is the exact byte
// sequence served by the manifest HTTP endpoint, including its trailing
// newline. SHA256 is lowercase hexadecimal over those same bytes.
//
// Callers must treat Body as immutable. The release-manifest tool base64
// encodes it so a non-Go caller can recover and verify the byte sequence
// without reserializing the JSON.
type ManifestDocument struct {
	Body   []byte
	SHA256 string
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
	return manifestHandler(id, slug, name, icon, tags, sqlFS, versions, reg, contribReg, nil)
}

// ManifestHandlerWithClient is the client-aware manifest entry used by the
// SDK's module wiring. ManifestHandler remains unchanged for source
// compatibility with callers that construct a manifest handler directly.
func ManifestHandlerWithClient(id, slug, name, icon string, tags []string, sqlFS fs.FS, versions map[string]MigrationVersions, reg *registry.Registry, contribReg *contributions.Registry, client *ClientSpec) http.HandlerFunc {
	return manifestHandler(id, slug, name, icon, tags, sqlFS, versions, reg, contribReg, client)
}

func manifestHandler(id, slug, name, icon string, tags []string, sqlFS fs.FS, versions map[string]MigrationVersions, reg *registry.Registry, contribReg *contributions.Registry, client *ClientSpec) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		document, err := BuildManifest(
			id, slug, name, icon, tags, sqlFS, versions, reg, contribReg, client,
		)
		if err != nil {
			log.Printf("mirrorstack: manifest marshal error: %v", err)
			httputil.JSON(w, http.StatusInternalServerError, httputil.ErrorResponse{Error: "manifest unavailable"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(manifestHashHeader, document.SHA256)
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(document.Body); err != nil {
			log.Printf("mirrorstack: manifest write error: %v", err)
		}
	}
}

// BuildManifest builds the exact document used by both the served manifest
// endpoint and the SDK's release-manifest tool mode. Keeping construction,
// encoding, and hashing here prevents the two release inputs from drifting.
//
// The encoding deliberately matches httputil.JSON: encoding/json's default
// HTML escaping and one trailing newline. Migration discovery errors remain
// non-fatal, matching the long-standing HTTP behavior; diagnostics are
// sanitized so an os.DirFS path is never disclosed.
func BuildManifest(id, slug, name, icon string, tags []string, sqlFS fs.FS, versions map[string]MigrationVersions, reg *registry.Registry, contribReg *contributions.Registry, client *ClientSpec) (ManifestDocument, error) {
	if versions == nil {
		versions = map[string]MigrationVersions{}
	}

	// Read each scope independently. The manifest still builds with an empty
	// version when the configured migration filesystem is unavailable.
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

	payload := ManifestPayload{
		ID:                id,
		Slug:              slug,
		Defaults:          ManifestDefaults{Name: name, Icon: icon, Tags: tags, NameLabels: i18n.Lookup("module.name"), TagLabels: i18n.LookupList(tagCatalogKey, tagLabelSep)},
		Description:       reg.Description(),
		DescriptionLabels: reg.DescriptionLabels(),
		Client:            client,
		Dependencies:      reg.Dependencies(),
		Migration:         MigrationVersions{App: appVersion, Module: moduleVersion},
		Versions:          versions,
		Routes:            reg.Routes(),
		Events:            ManifestEvents{Emits: reg.Emits(), Subscribes: reg.Subscribes()},
		Exposes:           ManifestExposes{Tables: reg.ExposedTables()},
		Schedules:         reg.Schedules(),
		Tasks:             reg.Tasks(),
		Permissions:       reg.Permissions(),
		Resources:         ManifestResources{Storage: reg.StorageRequired()},
		Metrics:           reg.Metrics(),
		AbsorbInfra:       reg.AbsorbsInfra(),
		MCP:               buildManifestMCP(reg),
		UI:                localizeUIPages(reg.UI()),
		Provides:          contribSlots,
		ContributesTo:     reg.OutboundContributions(),
	}

	// Marshal ONCE, hash exactly those bytes, then hand the SAME slice to
	// either the HTTP writer or release envelope.
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		return ManifestDocument{}, err
	}
	body := buf.Bytes()
	sum := sha256.Sum256(body)
	return ManifestDocument{Body: body, SHA256: hex.EncodeToString(sum[:])}, nil
}
