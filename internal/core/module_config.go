// Package mirrorstack is the Go SDK for building modules on MirrorStack.
//
// Use Init() + Start() for the convenience API, or New() for testing and advanced use.
package core

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/cache"
	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/contributions"
	"github.com/mirrorstack-ai/app-module-sdk/internal/httputil"
	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
	"github.com/mirrorstack-ai/app-module-sdk/internal/runtime"
	"github.com/mirrorstack-ai/app-module-sdk/meter"
	"github.com/mirrorstack-ai/app-module-sdk/storage"
	"github.com/mirrorstack-ai/app-module-sdk/system"
)

// This file holds the module's declared identity and the construction-time validation of it.
//
// Split out of a single 1069-line module.go — the SDK's own version of the
// catch-all this codebase tells module authors not to write.

// Config holds the module identity. Passed to Init() or New().
type Config struct {
	ID string // Immutable module identifier (required). Minted once by the
	// platform at first publish; never changes. Anchors the mod_<id> schema
	// and the catalog primary key.
	Slug string // Human-readable handle owned by the catalog. Mutable via
	// catalog UI; the storage prefix at install time is <username>_<slug>_.
	// Optional in dev; the publish pipeline is where slugs become required.
	Name string // Default display name (platform can override)
	Icon string // Default Material icon name (platform can override)

	// Description is a short, plain-language summary shown in the catalog/agent discovery. Optional.
	Description string

	// DescriptionLabel is the optional per-locale form of Description, built from
	// ms.Text (a literal) or ms.T (an i18n catalog key). When set it is resolved
	// against the module's i18n catalogs at manifest build and folded into the
	// manifest as descriptionLabels (locale → text) ALONGSIDE Description, so the
	// platform can show a localized summary. Description stays the default /
	// fallback; a zero Label is omitted entirely. Mirrors PermissionOpts.Description
	// / meter.MetricLabel. Resolution is lazy (manifest build), so RegisterMessages
	// may run before or after Init — only both-before-serve matters.
	DescriptionLabel Label

	// Tags are module-level category badges (e.g. "Auth", "Payments") shown in
	// the platform's module catalog / settings. Surfaced via manifest defaults.
	Tags []string

	// SQL is an optional filesystem containing the module's sql/ directory
	// (typically an embed.FS from `//go:embed sql/*`). The manifest endpoint
	// reads it to determine the latest migration version, and the lifecycle
	// routes (install/upgrade/downgrade) read it to apply migrations.
	SQL fs.FS

	// Versions optionally maps semver release tags to per-scope migration
	// numbers, e.g.:
	//
	//	{
	//	    "v0.1.0": {App: "0008", Module: "0002"},
	//	    "v0.2.0": {App: "0012"},  // module track unchanged
	//	}
	//
	// Exposed to the platform via the manifest endpoint so the platform can
	// translate its internal semver-based deploy state into the migration
	// numbers the lifecycle handlers accept. The SDK itself never reads this
	// map at lifecycle time — /lifecycle/{upgrade,downgrade} take migration
	// numbers only.
	Versions map[string]system.MigrationVersions

	// WebDir is the on-disk path to the module's React bundle output
	// (typically "web/dist"). When set, the SDK serves it publicly under
	// /__mirrorstack/web/* with permissive CORS so the platform's catch-all
	// route can dynamically import the named exports declared in
	// RegisterUI.DefaultPages. Optional — when empty, the /web route 404s.
	WebDir string

	// Client declares an optional custom-app client project. Dir is relative to
	// the module root; OutputDir is relative to Dir. The CLI builds and publishes
	// this output during `mirrorstack dev --tunnel`. Package identity, versions,
	// registry details, and the build command are controlled by MirrorStack.
	Client *system.ClientSpec
}

// Module is the core SDK instance.
//
// internalAuth is captured at New() time so OnEvent/Cron registrations can
// reuse a single middleware closure. auth.InternalAuth() reads
// MS_INTERNAL_SECRET once at construction; constructing it per registration
// would re-read the env var and re-allocate the closure on every call.
type Module struct {
	config         Config
	router         *chi.Mux
	logger         *log.Logger
	registry       *registry.Registry
	contribReg     *contributions.Registry // declared contribution slots
	contribStorage *contributions.Storage  // contributions table CRUD
	internalAuth   func(http.Handler) http.Handler
	// proxyGuard rejects non-proxied requests on the public + platform
	// surfaces (auth.RequireProxy). Captured once at New() so every
	// Public/Platform registration reuses one closure, matching internalAuth.
	proxyGuard func(http.Handler) http.Handler
	// platformAuth promotes trusted-forwarder identity headers on the platform
	// surface (auth.PlatformAuth). Captured once at New() — same "captured
	// once" contract as internalAuth/proxyGuard — so every Platform()
	// registration reuses one closure instead of re-reading env + re-allocating
	// the closure (and its secret snapshot) on each call.
	platformAuth   func(http.Handler) http.Handler
	poolCache      *db.PoolCache // production: per-app DB pools
	devDBOnce      sync.Once     // dev mode: lazy DB init
	devDB          *db.DB
	devDBErr       error
	cacheCache     *cache.ClientCache // production: per-app Redis clients
	devCacheOnce   sync.Once          // dev mode: lazy cache init
	devCache       *cache.Client
	devCacheErr    error
	devStorageOnce sync.Once // dev mode: lazy storage init
	devStorage     *storage.DevMinter
	devStorageErr  error
	taskHandlers   map[string]taskEntry // registered task handlers (startup-only writes)
	localTasks     *localTaskRegistry   // dev-only jobs; managed calls never consult this registry
	taskHTTPClient *http.Client         // managed enqueue transport; replaceable in focused tests
	meterClient    *meter.Client        // dispatch-HTTP usage transport (dev + prod); never nil

	// devMode is true under `mirrorstack dev` (HTTP serving + dev DB), false in
	// Lambda/task-worker. It gates the dev-only per-app schema machinery:
	// production injects the app schema via the Lambda shim, dev derives it from
	// X-MS-App-ID here. Captured once at New().
	devMode bool
	// devProvision caches per-app schema provisioning (schema string ->
	// *devProvisionEntry) so only the first dev request for an app runs its
	// app-scope migrations. See Module.ensureDevAppSchema.
	devProvision sync.Map
	// devDir is the co-located dev dependency directory: the lookup/ensure/
	// publish seams, the lease heartbeat's lifecycle channels, and the two
	// caches that keep the read path off Postgres. See dev_directory.go.
	// Allocated in New(); never nil.
	devDir *devDirectoryState
}

// devAppSchemaName derives the per-app Postgres schema from an app id,
// delegating to runtime.AppSchemaName so this package and system.SeedHandler
// (which derives the same schema from the dev-mount seed contract's bare
// appId) can never drift on the shape. pgx.Identifier.Sanitize is the second
// line of defense before SQL. Returns ok=false if the result is not a valid
// schema identifier.
func devAppSchemaName(appID string) (string, bool) {
	return runtime.AppSchemaName(appID)
}

// devAppSchemaMiddleware is the dev analog of the Lambda shim's per-request
// AppSchema injection (see runtime.InjectResources). For requests carrying an
// app identity (X-MS-App-ID, forwarded by dispatch) it derives app_<id>,
// provisions it lazily on first touch, and injects it via db.WithSchema so
// Module.DB/Module.Tx scope to that app's schema. Attached only in devMode; in
// production the schema is already in context and the X-MS-* headers are
// stripped, so this never runs there.
func (m *Module) devAppSchemaMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appID := r.Header.Get(auth.HeaderAppID)
		if appID == "" || db.SchemaFrom(r.Context()) != "" {
			next.ServeHTTP(w, r)
			return
		}
		schema, ok := devAppSchemaName(appID)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		// Provision off the request context: migrations must not be cancelled by
		// a client disconnect, and the cached result must not memoize a
		// cancellation error for every later request.
		if err := m.ensureDevAppSchema(context.Background(), schema); err != nil {
			m.logger.Printf("dev: provision app schema %s: %v", schema, err)
			httputil.JSON(w, http.StatusInternalServerError, httputil.ErrorResponse{Error: "app schema provisioning failed"})
			return
		}
		next.ServeHTTP(w, r.WithContext(db.WithSchema(r.Context(), schema)))
	})
}

// moduleIDPattern matches valid module IDs: lowercase letter, then lowercase alphanumerics/underscores, max 36 chars.
// 36 chars accommodates UUID-derived IDs ("m" + 32 hex digits = 33 chars) with
// a small safety margin. The "mod_" prefix the SDK adds when constructing
// schema names still fits comfortably under Postgres's 63-char identifier limit.
var moduleIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,35}$`)

// moduleSlugPattern matches catalog slugs: lowercase letter, then lowercase
// alphanumerics/hyphens, max 16 chars. The 16-char cap keeps the worst-case
// constructed identifier `<username>_<slug>_<table>_<col>_fkey` inside
// Postgres's 63-byte NAMEDATALEN ceiling. The CLI's publish-time linter is
// the real gate; this regex catches obvious shape errors at New() time.
var moduleSlugPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,15}$`)

func validateClientPath(field, value string) error {
	if value == "" || value == "." || strings.ContainsAny(value, "\\:\x00") || !fs.ValidPath(value) {
		return fmt.Errorf("mirrorstack: Config.Client.%s %q must be a non-empty canonical relative path using forward slashes", field, value)
	}
	return nil
}

func validateClientSpec(client *system.ClientSpec) error {
	if client == nil {
		return nil
	}
	if err := validateClientPath("Dir", client.Dir); err != nil {
		return err
	}
	return validateClientPath("OutputDir", client.OutputDir)
}

// New creates a new Module.
