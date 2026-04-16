// Package mirrorstack is the Go SDK for building modules on MirrorStack.
//
// Use Init() + Start() for the convenience API, or New() for testing and advanced use.
package mirrorstack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	runtimepkg "runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/invopop/jsonschema"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/go-chi/chi/v5"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/cache"
	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/httputil"
	"github.com/mirrorstack-ai/app-module-sdk/internal/migration"
	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
	"github.com/mirrorstack-ai/app-module-sdk/internal/runtime"
	msqs "github.com/mirrorstack-ai/app-module-sdk/internal/sqs"
	"github.com/mirrorstack-ai/app-module-sdk/meter"
	"github.com/mirrorstack-ai/app-module-sdk/roles"
	"github.com/mirrorstack-ai/app-module-sdk/storage"
	"github.com/mirrorstack-ai/app-module-sdk/system"
)

// Config holds the module identity. Passed to Init() or New().
type Config struct {
	ID   string // Unique module identifier (required)
	Name string // Default display name (platform can override)
	Icon string // Default Material icon name (platform can override)

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
	internalAuth   func(http.Handler) http.Handler
	poolCache      *db.PoolCache // production: per-app DB pools
	devDBOnce      sync.Once     // dev mode: lazy DB init
	devDB          *db.DB
	devDBErr       error
	cacheCache     *cache.ClientCache // production: per-app Redis clients
	devCacheOnce   sync.Once          // dev mode: lazy cache init
	devCache       *cache.Client
	devCacheErr    error
	devStorageOnce sync.Once // dev mode: lazy storage init
	devStorage     *storage.Client
	devStorageErr  error
	taskHandlers   map[string]taskEntry // registered task handlers (startup-only writes)
	sqsClient      *msqs.Client         // nil in dev mode (MS_TASK_QUEUE_URL unset)
	signingKey     []byte               // HMAC key for TaskMessage signing (MS_TASK_SIGNING_KEY)
	meterClient    *meter.Client        // prod (MS_METER_LAMBDA_ARN set) or dev-mode stderr
}

// moduleIDPattern matches valid module IDs: lowercase letter, then lowercase alphanumerics/underscores, max 31 chars.
// Leaves room for the "mod_" prefix without exceeding Postgres's 63-char identifier limit.
var moduleIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,30}$`)

// New creates a new Module.
func New(cfg Config) (*Module, error) {
	if cfg.ID == "" {
		return nil, errors.New("mirrorstack: Config.ID is required")
	}
	if !moduleIDPattern.MatchString(cfg.ID) {
		return nil, fmt.Errorf("mirrorstack: Config.ID %q must match %s (lowercase, starts with letter, max 31 chars)", cfg.ID, moduleIDPattern)
	}
	m := &Module{
		config:       cfg,
		router:       chi.NewRouter(),
		logger:       log.New(os.Stderr, "mirrorstack: ", log.LstdFlags),
		registry:     registry.New(),
		internalAuth: auth.InternalAuth(),
		poolCache:    db.NewPoolCache(),
		cacheCache:   cache.NewClientCache(),
		taskHandlers: make(map[string]taskEntry),
		signingKey:   []byte(os.Getenv("MS_TASK_SIGNING_KEY")),
	}

	// Eagerly initialize SQS client when queue URL is configured.
	// LoadDefaultConfig may hit IMDS on first cold start in Lambda; acceptable
	// since this runs once at process init, not per request.
	if queueURL := os.Getenv("MS_TASK_QUEUE_URL"); queueURL != "" {
		sqsClient, err := msqs.New(context.Background(), queueURL)
		if err != nil {
			return nil, fmt.Errorf("mirrorstack: init sqs client: %w", err)
		}
		m.sqsClient = sqsClient
	}

	// Meter client: production mode when MS_METER_LAMBDA_ARN is set (ARN is
	// validated at construction so typos fail fast), dev-mode stderr sink
	// otherwise. Never nil.
	if meterARN := os.Getenv("MS_METER_LAMBDA_ARN"); meterARN != "" {
		meterClient, err := meter.NewFromARN(context.Background(), meterARN)
		if err != nil {
			return nil, fmt.Errorf("mirrorstack: init meter client: %w", err)
		}
		m.meterClient = meterClient
	} else {
		m.meterClient = meter.NewDev(m.logger)
	}

	m.mountSystemRoutes()
	return m, nil
}

func (m *Module) Config() Config   { return m.config }
func (m *Module) Router() *chi.Mux { return m.router }

// DB returns a scoped database connection.
//
// Production: uses per-app credentials injected by the platform via Lambda payload.
// Dev: uses DATABASE_URL env var with localhost fallback.
//
//	conn, release, err := mod.DB(r.Context())
//	if err != nil { ... }
//	defer release()
//	conn.QueryRow(ctx, "SELECT ...").Scan(&v)
func (m *Module) DB(ctx context.Context) (db.Querier, func(), error) {
	pool, releasePool, err := m.resolvePool(ctx)
	if err != nil {
		return nil, nil, err
	}
	querier, releaseConn, err := db.AcquireScoped(ctx, pool)
	if err != nil {
		releasePool()
		return nil, nil, err
	}
	return querier, func() {
		releaseConn()
		releasePool()
	}, nil
}

// Tx runs fn inside a transaction with schema isolation. Commits on success,
// rolls back on error. The app schema is read from the caller's context (set
// by the platform's Lambda invoke shim via db.WithSchema). Compare with
// Module.ModuleTx which explicitly overlays the mod_<id> schema.
//
//	err := mod.Tx(r.Context(), func(q db.Querier) error {
//	    queries := generated.New(q)
//	    item, err := queries.GetItem(ctx, id)
//	    if err != nil { return err }
//	    return queries.DeductBalance(ctx, params)
//	})
func (m *Module) Tx(ctx context.Context, fn func(q db.Querier) error) error {
	pool, releasePool, err := m.resolvePool(ctx)
	if err != nil {
		return err
	}
	defer releasePool()
	return db.Tx(ctx, pool, fn)
}

// resolvePool returns the per-app credential pool (production) or the dev
// pool (dev mode). See resolvePoolFor for the shared logic.
func (m *Module) resolvePool(ctx context.Context) (*pgxpool.Pool, func(), error) {
	return m.resolvePoolFor(ctx, db.CredentialFrom)
}

// resolvePoolFor is the shared implementation behind resolvePool and
// resolveModulePool. Production reads a credential from the context via
// getCred (different context key per scope) and pulls a refcount-pinned
// pool from the cache. Dev mode falls through to the single dev pool, which
// is shared across all scopes — schema isolation in dev happens at the
// AcquireScoped layer via WithSchema, not at the pool level.
func (m *Module) resolvePoolFor(ctx context.Context, getCred func(context.Context) *db.Credential) (*pgxpool.Pool, func(), error) {
	if cred := getCred(ctx); cred != nil {
		return m.poolCache.Get(ctx, *cred)
	}
	m.devDBOnce.Do(func() {
		m.devDB, m.devDBErr = db.Open(context.Background())
	})
	if m.devDBErr != nil {
		return nil, nil, m.devDBErr
	}
	return m.devDB.Pool(), func() {}, nil
}

// ModuleDB returns a connection scoped to this module's shared schema
// (mod_<id>). Use it for cross-app state — outbox tables, dedup ledgers,
// cross-app audit logs, rate limiters, module-wide config — anything a
// module needs that is owned by the module rather than by a single app.
//
// Production: uses the per-module credential injected by the platform via
// db.WithModuleCredential. Independent of the per-app credential read by
// Module.DB — a handler that needs both gets both, in the same context.
//
// Dev: shares the dev pool from DATABASE_URL with Module.DB; the schema is
// the only difference. The dev Postgres must have a mod_<id> schema for
// the queries to succeed.
//
// The module schema overlays any app schema set on the caller's context for
// this single call only — the caller's ctx is not mutated, so a subsequent
// Module.DB call sees the original app schema unchanged.
//
//	conn, release, err := mod.ModuleDB(r.Context())
//	if err != nil { ... }
//	defer release()
//	conn.Exec(ctx, "INSERT INTO outbox(...) VALUES(...)")
func (m *Module) ModuleDB(ctx context.Context) (db.Querier, func(), error) {
	pool, releasePool, err := m.resolveModulePool(ctx)
	if err != nil {
		return nil, nil, err
	}
	moduleCtx := db.WithSchema(ctx, m.moduleSchema())
	querier, releaseConn, err := db.AcquireScoped(moduleCtx, pool)
	if err != nil {
		releasePool()
		return nil, nil, err
	}
	return querier, func() {
		releaseConn()
		releasePool()
	}, nil
}

// ModuleTx runs fn inside a transaction scoped to the module's shared schema
// (mod_<id>). Commits on success, rolls back on error or panic. Use this for
// the outbox-pattern: insert the work record AND the dedup row in the same
// transaction so the worker only ever sees consistent state.
//
//	err := mod.ModuleTx(r.Context(), func(q db.Querier) error {
//	    queries := generated.New(q)
//	    if err := queries.InsertOutbox(ctx, ...); err != nil { return err }
//	    return queries.MarkProcessed(ctx, ...)
//	})
func (m *Module) ModuleTx(ctx context.Context, fn func(q db.Querier) error) error {
	pool, releasePool, err := m.resolveModulePool(ctx)
	if err != nil {
		return err
	}
	defer releasePool()
	moduleCtx := db.WithSchema(ctx, m.moduleSchema())
	return db.Tx(moduleCtx, pool, fn)
}

// resolveModulePool reads the per-module credential instead of the per-app
// one. See resolvePoolFor for the shared logic.
func (m *Module) resolveModulePool(ctx context.Context) (*pgxpool.Pool, func(), error) {
	return m.resolvePoolFor(ctx, db.ModuleCredentialFrom)
}

// moduleSchema returns the Postgres schema name for this module's shared
// state. Convention: "mod_" + the developer's Config.ID. The platform must
// pre-create this schema and grant the per-module DB role USAGE on it
// before any module handler runs.
func (m *Module) moduleSchema() string {
	return "mod_" + m.config.ID
}

// lifecycleTxRunner returns the TxRunner that should drive migrations for
// the given scope. Each scope runs against a different schema and uses a
// different DB credential, so the two scopes require different runners:
//
//   - ScopeApp → Module.Tx (reads db.CredentialFrom, per-app role, app_<id>
//     schema from ctx).
//   - ScopeModule → Module.ModuleTx (reads db.ModuleCredentialFrom, per-module
//     role, mod_<id> schema overlayed inside the transaction).
//
// Mixing these up is silently wrong: module migrations driven by the app
// credential would fail at Postgres because the per-(module, app) role lacks
// USAGE on mod_<id>, but that is infrastructure defense-in-depth — the SDK
// must pick the correct runner itself.
func (m *Module) lifecycleTxRunner(scope migration.Scope) migration.TxRunner {
	switch scope {
	case migration.ScopeModule:
		return m.ModuleTx
	case migration.ScopeApp:
		return m.Tx
	default:
		panic("mirrorstack: lifecycleTxRunner: unhandled scope " + string(scope))
	}
}

// Cache returns a scoped cache client. Keys are auto-prefixed with {appID}:{moduleID}:.
//
//	c, release, err := mod.Cache(r.Context())
//	if err != nil { ... }
//	defer release()
//	c.Set(ctx, "views:123", "42", 5*time.Minute)
//	val, err := c.Get(ctx, "views:123")
func (m *Module) Cache(ctx context.Context) (cache.Cacher, func(), error) {
	client, releaseClient, err := m.resolveCache(ctx)
	if err != nil {
		return nil, nil, err
	}
	// Always apply prefix — never return unprefixed base client
	appID := ""
	if a := auth.Get(ctx); a != nil {
		appID = a.AppID
	}
	return client.ForApp(appID, m.config.ID), releaseClient, nil
}

// resolveCache returns the underlying cache client and a release closure.
// Production uses ClientCache (refcount-pinned). Dev uses a single shared
// client (no-op release).
func (m *Module) resolveCache(ctx context.Context) (*cache.Client, func(), error) {
	if cred := cache.CredentialFrom(ctx); cred != nil {
		return m.cacheCache.Get(ctx, *cred)
	}
	m.devCacheOnce.Do(func() {
		m.devCache, m.devCacheErr = cache.Open(context.Background())
	})
	if m.devCacheErr != nil {
		return nil, nil, m.devCacheErr
	}
	return m.devCache, func() {}, nil
}

// Storage returns a scoped storage client. Keys are auto-prefixed with the app/module path.
//
//	s, err := mod.Storage(r.Context())
//	if err != nil { ... }
//	url, err := s.PresignPut(ctx, "photo.jpg", 15*time.Minute)
//	cdnURL, err := s.URL("photo.jpg")
//
// Prefix and CDN base come from the per-invocation STS credential in production,
// or env vars in dev mode. resolveStorage handles both paths — NewFromCredential
// already sets the prefix from cred.Prefix, so no separate ForApp scoping is needed.
func (m *Module) Storage(ctx context.Context) (storage.Storer, error) {
	return m.resolveStorage(ctx)
}

// Meter returns a scoped meter for recording usage events (billing metrics).
// Unlike DB/Cache/Storage, Meter returns the interface directly — there is
// no release closure (nothing to release) and no construction error
// (init errors happen eagerly in New()).
//
// In production (MS_METER_LAMBDA_ARN set), Record dispatches to the platform
// meter Lambda via async invoke (~5-15ms per call). In dev mode, Record
// logs to stderr.
//
//	if err := ms.Meter(r.Context()).Record("transcode.minutes", 12); err != nil {
//	    log.Printf("meter: %v", err) // don't fail the handler
//	}
func (m *Module) Meter(ctx context.Context) meter.Meter {
	return m.meterClient.Scope(ctx, m.config.ID)
}

func (m *Module) resolveStorage(ctx context.Context) (*storage.Client, error) {
	// Production: STS credentials from Lambda payload.
	// No caching — S3 client creation is cheap (no I/O), and STS tokens rotate
	// frequently. Caching by AccessKeyID risks using stale credentials.
	if cred := storage.CredentialFrom(ctx); cred != nil {
		return storage.NewFromCredential(*cred)
	}
	// Dev: env vars
	m.devStorageOnce.Do(func() {
		m.devStorage, m.devStorageErr = storage.Open(context.Background())
	})
	return m.devStorage, m.devStorageErr
}

// Platform registers routes with platform auth scope.
// Default: admin only. Use Module.RequirePermission for member/viewer access.
func (m *Module) Platform(fn func(r chi.Router)) {
	m.scopedRoutes(registry.ScopePlatform, auth.PlatformAuth(), fn)
}

// Public registers routes with public auth scope (anyone, including anonymous).
func (m *Module) Public(fn func(r chi.Router)) {
	m.scopedRoutes(registry.ScopePublic, nil, fn)
}

// Internal registers routes with internal auth scope (platform-to-module only).
// Validates X-MS-Internal-Secret via constant-time comparison. The middleware
// is cached on the Module at New() so OnEvent / Cron registrations reuse a
// single closure instead of constructing one per call.
func (m *Module) Internal(fn func(r chi.Router)) {
	m.scopedRoutes(registry.ScopeInternal, m.internalAuth, fn)
}

// scopedRoutes records every route fn registers under the given scope, then
// re-attaches them to the main router with the scope's auth middleware.
//
// We use a sub-router + chi.Walk so the manifest endpoint can list every
// declared route per scope. Walking after fn() returns gives us the full
// route table (chi accumulates path prefixes from r.Route automatically) plus
// each route's middleware chain, which we re-apply on the main router via
// .With(...).Method(...) so the routing behavior is identical to the previous
// .Group()-based implementation.
//
// We rely on chi.Walk including the sub-router's Use() middlewares in the
// callback's middlewares slice — this is how chi v5 propagates route-level
// middleware chains. If a future chi version changes that behavior,
// TestManifest_RegisteredScopesStillRouteCorrectly is the regression guard
// (it asserts platform routes still return 401 without auth).
//
// chi.Walk only returns an error if the WalkFunc returns one — ours never
// does. A non-nil error here would mean chi itself failed to walk its own
// route tree, which indicates a misconfigured module that should not start.
// Panic instead of silently leaving the registry and router in inconsistent
// states (some routes recorded but not re-registered, or vice versa).
// internalRouteBodyCap is the default body size limit for Internal-scope
// routes (events, crons, tasks, and developer-registered internal handlers).
// Defense-in-depth — Lambda's API Gateway has a 6 MB cap, but dev mode is
// unbounded without this.
const internalRouteBodyCap = 1 << 20 // 1 MB

func (m *Module) scopedRoutes(scope registry.Scope, scopeMiddleware func(http.Handler) http.Handler, fn func(r chi.Router)) {
	sub := chi.NewRouter()
	if scope == registry.ScopeInternal {
		sub.Use(httputil.MaxBytes(internalRouteBodyCap))
	}
	if scopeMiddleware != nil {
		sub.Use(scopeMiddleware)
	}
	fn(sub)

	if err := chi.Walk(sub, func(method, route string, handler http.Handler, middlewares ...func(http.Handler) http.Handler) error {
		m.registry.AddRoute(scope, method, route)
		m.router.With(middlewares...).Method(method, route, handler)
		return nil
	}); err != nil {
		panic("mirrorstack: scopedRoutes(" + string(scope) + ") chi.Walk failed: " + err.Error())
	}
}

// RequirePermission returns chi middleware that checks AppRole against the
// allowed roles AND records the permission on this Module's registry so it
// appears in the manifest payload. Call this at route registration time
// (alongside m.Platform/Public/Internal), NOT from inside a request handler
// — registry append is O(N), so dynamic per-request names would leak memory
// and slow down every subsequent registration.
//
// Roles are typed values from the roles package (Admin, Viewer, Custom) to
// prevent typos and enable IDE autocomplete:
//
//	import p "github.com/mirrorstack-ai/app-module-sdk/roles"
//	r.With(mod.RequirePermission("media.view", p.Admin(), p.Viewer())).Get("/items", listItems)
func (m *Module) RequirePermission(name string, allowed ...roles.Role) func(http.Handler) http.Handler {
	keys := roles.Keys(allowed)
	m.registry.AddPermission(name, keys)
	return auth.RequireRoles(keys...)
}

// RequirePermission is the convenience wrapper that dispatches to the default
// Module created by Init(). Calling this before Init() panics — match the
// behavior of Platform/Public/Internal.
//
//	import p "github.com/mirrorstack-ai/app-module-sdk/roles"
//
//	ms.Init(...)
//	ms.Platform(func(r chi.Router) {
//	    r.With(ms.RequirePermission("media.view", p.Admin(), p.Viewer())).Get(...)
//	})
func RequirePermission(name string, allowed ...roles.Role) func(http.Handler) http.Handler {
	return mustDefault("RequirePermission").RequirePermission(name, allowed...)
}

// Describe sets the module's human-readable description. Used by the catalog
// for agent discovery ("find a module that does X"). One to three sentences is
// typical. Last call wins; typically called once at module init.
//
//	ms.Describe("Google OAuth provider: authorize, callback, session issue.")
func (m *Module) Describe(s string) {
	m.registry.SetDescription(s)
}

// DependsOn declares a dependency on another module by ID. The required vs
// optional distinction is AUTO-DETECTED from the call site via the Go stack:
//
//   - Called from main.main or a package-level init() → REQUIRED (catalog
//     installs the dependency before this module; install fails without it).
//   - Called from anywhere else (helper functions, setup routines, handlers)
//     → OPTIONAL (this module runs standalone; use ms.Resolve[T] to
//     opportunistically use the dependency at runtime if installed).
//
// Caveat: an "extract function" refactor that moves a DependsOn call out of
// main() into a helper silently changes the dep from required to optional.
// Keep required deps declared literally in main() (or a package-level init()).
// Escape hatch: wrap required deps in an init() if they must live outside
// main, e.g. for a shared registration package.
//
//	func main() {
//	    ms.Init(...)
//	    ms.DependsOn("oauth-core")       // required — caller is main
//	    registerVideoIntegration()
//	    ms.Start()
//	}
//
//	func registerVideoIntegration() {
//	    ms.DependsOn("video")            // optional — caller is helper
//	    ms.OnEvent("video.completed", handleVideoCompleted)
//	}
func (m *Module) DependsOn(id string) {
	optional := !callerIsRoot()
	m.registry.AddDependency(id, optional)
}

// Describe is the convenience wrapper that dispatches to the default Module.
// Calling before Init() panics.
func Describe(s string) { mustDefault("Describe").Describe(s) }

// DependsOn is the convenience wrapper that dispatches to the default Module.
// Calling before Init() panics. See Module.DependsOn for the auto-detect rule.
func DependsOn(id string) { mustDefault("DependsOn").DependsOn(id) }

// Resolve looks up a previously-registered client of type T by module ID.
// Returns the zero value of T and false when no client is registered for id
// (either because the dependency module isn't installed or no module has
// exported a T-typed client).
//
// Pairs with DependsOn(id) declared optional: check ok before calling into T.
//
//	if user, ok := ms.Resolve[userclient.Client]("user"); ok {
//	    user.UpsertByExternalIdentity(ctx, ext)
//	} else {
//	    // fallback: platform identity resolution
//	}
//
// v1 note: cross-module client wiring is not yet designed. This stub always
// returns (zero, false). Real resolution lands with the catalog's install
// machinery.
func Resolve[T any](id string) (T, bool) {
	var zero T
	return zero, false
}

// callerIsRoot inspects the stack and returns true when the user-code caller
// of DependsOn is main.main or a package-level init function. SDK frames
// (the DependsOn method itself, the package-level wrapper) are skipped.
func callerIsRoot() bool {
	pcs := make([]uintptr, 32)
	// Skip: runtime.Callers (0), this function (1). Start at the caller of
	// callerIsRoot, which is DependsOn.
	n := runtimepkg.Callers(2, pcs)
	if n == 0 {
		return false
	}
	frames := runtimepkg.CallersFrames(pcs[:n])
	for {
		frame, more := frames.Next()
		fn := frame.Function
		// Skip the DependsOn plumbing frames (both receiver method and
		// package-level wrapper end with ".DependsOn").
		if strings.HasSuffix(fn, ".DependsOn") {
			if !more {
				return false
			}
			continue
		}
		return isRootCallerName(fn)
	}
}

// isRootCallerName reports whether fn is main.main or any package init().
// Go compiles init functions as "pkg.init", "pkg.init.0", "pkg.init.1" etc.
func isRootCallerName(fn string) bool {
	if fn == "main.main" {
		return true
	}
	idx := strings.LastIndex(fn, ".init")
	if idx < 0 {
		return false
	}
	rest := fn[idx+len(".init"):]
	if rest == "" {
		return true
	}
	if !strings.HasPrefix(rest, ".") {
		return false
	}
	for _, c := range rest[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// MCPTool registers an agent-callable tool on the default module. Input and
// output JSON Schemas are derived from the In and Out type parameters via
// reflection; struct fields use their `json:"..."` tags. The handler receives
// parsed typed args and returns a typed result.
//
// Name must satisfy registry.ValidateName (no path separators, whitespace, or
// null bytes). First-wins: a duplicate registration is a no-op.
//
// The tool is served at POST /__mirrorstack/mcp/tools/call under Internal
// scope. The platform aggregates tools from all installed modules into a
// single agent-facing MCP server.
//
//	type GreetArgs struct{ Name string `json:"name"` }
//	type GreetResult struct{ Message string `json:"message"` }
//	ms.MCPTool("greet", "Say hi to someone",
//	    func(ctx context.Context, a GreetArgs) (GreetResult, error) {
//	        return GreetResult{Message: "hi " + a.Name}, nil
//	    })
//
// Panics before Init or on schema derivation failure.
func MCPTool[In, Out any](name, description string, handler func(ctx context.Context, args In) (Out, error)) {
	m := mustDefault("MCPTool")
	inputSchema, err := deriveSchema[In]()
	if err != nil {
		panic("mirrorstack: MCPTool(" + name + ") input schema derivation failed: " + err.Error())
	}
	outputSchema, err := deriveSchema[Out]()
	if err != nil {
		panic("mirrorstack: MCPTool(" + name + ") output schema derivation failed: " + err.Error())
	}
	m.registry.AddMCPTool(registry.MCPToolDecl{
		Name:         name,
		Description:  description,
		InputSchema:  inputSchema,
		OutputSchema: outputSchema,
		Handler:      wrapMCPToolHandler(handler),
	})
}

// MCPResource registers an agent-readable resource on the default module. The
// handler returns current content on demand. Output schema is derived from Out.
// Panics before Init or on schema derivation failure.
func MCPResource[Out any](name, description string, handler func(ctx context.Context) (Out, error)) {
	m := mustDefault("MCPResource")
	schema, err := deriveSchema[Out]()
	if err != nil {
		panic("mirrorstack: MCPResource(" + name + ") schema derivation failed: " + err.Error())
	}
	m.registry.AddMCPResource(registry.MCPResourceDecl{
		Name:        name,
		Description: description,
		Schema:      schema,
		Handler:     wrapMCPResourceHandler(handler),
	})
}

// deriveSchema returns a JSON Schema for T via reflection over struct tags.
func deriveSchema[T any]() (json.RawMessage, error) {
	var zero T
	schema := jsonschema.Reflect(zero)
	return json.Marshal(schema)
}

// wrapMCPToolHandler adapts a typed handler into the type-erased registry form.
// Args unmarshal failures become ErrInvalidArgs (400); handler errors pass
// through (default to 500 unless the handler returned ErrInvalidArgs itself).
func wrapMCPToolHandler[In, Out any](handler func(context.Context, In) (Out, error)) registry.MCPToolHandler {
	return func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
		var in In
		if len(args) > 0 && string(args) != "null" {
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, fmt.Errorf("%w: %s", system.ErrInvalidArgs, err.Error())
			}
		}
		out, err := handler(ctx, in)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	}
}

// wrapMCPResourceHandler adapts a typed resource handler into the registry form.
func wrapMCPResourceHandler[Out any](handler func(context.Context) (Out, error)) registry.MCPResourceHandler {
	return func(ctx context.Context) (json.RawMessage, error) {
		out, err := handler(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	}
}

// Start auto-detects the runtime mode and starts serving:
//
//   - Lambda (AWS_LAMBDA_FUNCTION_NAME set): wraps the router for Lambda invoke
//   - Task worker (MS_TASK_WORKER_MODE=true): polls SQS for background tasks
//   - Otherwise: HTTP server on :PORT (default 8080) for local development
//
// Lambda wins if both env vars are set (they are mutually exclusive in
// production but this ordering is a safety net).
func (m *Module) Start() error {
	if runtime.IsLambda() {
		if err := requireInternalSecret(); err != nil {
			return err
		}
		handler := runtime.NewLambdaHandler(m.router)
		lambda.Start(handler)
		return nil
	}

	if runtime.IsTaskWorker() {
		return m.startTaskWorker()
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port
	m.logger.Printf("%s module (%s) listening on %s", m.config.Name, m.config.ID, addr)
	if err := http.ListenAndServe(addr, m.router); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// startTaskWorker runs the SQS poll loop. Spawns MS_TASK_CONCURRENCY
// goroutines (default 1). Blocks until the context is cancelled (SIGTERM
// handling is added in PR 4).
func (m *Module) startTaskWorker() error {
	if m.sqsClient == nil {
		return errors.New("mirrorstack: MS_TASK_QUEUE_URL is required in task worker mode")
	}
	if len(m.signingKey) < 32 {
		return errors.New("mirrorstack: MS_TASK_SIGNING_KEY must be at least 32 bytes in task worker mode")
	}
	if len(m.taskHandlers) == 0 {
		return errors.New("mirrorstack: no tasks registered via OnTask — nothing to process")
	}

	concurrency, err := parseTaskConcurrency()
	if err != nil {
		return err
	}

	// Build the handler map in the shape the worker loop expects.
	handlers := make(map[string]runtime.TaskEntry, len(m.taskHandlers))
	for name, entry := range m.taskHandlers {
		handlers[name] = runtime.TaskEntry{
			Handler: runtime.TaskHandlerFunc(entry.handler),
			Timeout: entry.timeout,
		}
	}

	cfg := runtime.WorkerConfig{
		SQSClient:    m.sqsClient,
		Handlers:     handlers,
		SigningKey:   m.signingKey,
		Logger:       m.logger,
		IsProduction: true, // MS_TASK_QUEUE_URL is set (checked above)
	}

	m.logger.Printf("%s module (%s) starting task worker (concurrency=%d)", m.config.Name, m.config.ID, concurrency)

	// SIGTERM/SIGINT: stop accepting new messages, drain in-flight handlers.
	// ECS sends SIGTERM 30s before SIGKILL; we use a 25s drain window to
	// leave a 5s buffer for Close() and process exit.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	var wg sync.WaitGroup
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runtime.PollLoop(ctx, &cfg)
		}()
	}

	// Block until shutdown signal.
	<-ctx.Done()
	stop() // release signal channel early
	m.logger.Printf("mirrorstack: shutdown signal received, draining tasks (max 25s)")

	// Wait for all poll goroutines to exit (each exits when ctx.Done fires
	// and their current message finishes or times out).
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer drainCancel()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
		m.logger.Printf("mirrorstack: all tasks drained cleanly")
	case <-drainCtx.Done():
		m.logger.Printf("mirrorstack: drain timeout exceeded — some goroutines may still be running")
	}

	m.Close()
	return nil
}

// parseTaskConcurrency reads MS_TASK_CONCURRENCY, defaulting to 1.
// Returns an error for non-integer or < 1 values.
func parseTaskConcurrency() (int, error) {
	s := os.Getenv("MS_TASK_CONCURRENCY")
	if s == "" {
		return 1, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("mirrorstack: MS_TASK_CONCURRENCY=%q is not a valid integer", s)
	}
	if n < 1 {
		return 0, fmt.Errorf("mirrorstack: MS_TASK_CONCURRENCY=%d must be >= 1", n)
	}
	return n, nil
}

// requireInternalSecret errors if MS_INTERNAL_SECRET is unset — used by
// Module.Start() in Lambda mode to fail init before lambda.Start handoff.
func requireInternalSecret() error {
	if os.Getenv("MS_INTERNAL_SECRET") == "" {
		return errors.New("mirrorstack: MS_INTERNAL_SECRET not set — required for platform routes in Lambda mode")
	}
	return nil
}

// Close cleans up resources.
func (m *Module) Close() {
	if m.poolCache != nil {
		m.poolCache.Close()
	}
	if m.devDB != nil {
		m.devDB.Close()
	}
	if m.cacheCache != nil {
		m.cacheCache.Close()
	}
	if m.devCache != nil {
		m.devCache.Close()
	}
}

func (m *Module) mountSystemRoutes() {
	m.router.Route("/__mirrorstack", func(r chi.Router) {
		r.Get("/health", system.Health) // intentionally public — no auth
		r.Route("/platform", func(r chi.Router) {
			r.Use(httputil.MaxBytes(64 * 1024)) // 64 KB — lifecycle bodies are tiny
			r.Use(m.internalAuth)
			r.Get("/manifest", system.ManifestHandler(
				m.config.ID, m.config.Name, m.config.Icon,
				m.config.SQL, m.config.Versions, m.registry,
			))
			r.Route("/lifecycle", func(r chi.Router) {
				// App and module migrations are separate tracks on disjoint
				// directories AND disjoint DB credentials; mount the same
				// four endpoints under each scope so the platform can drive
				// them independently.
				for _, scope := range migration.AllScopes() {
					runTx := m.lifecycleTxRunner(scope)
					r.Route("/"+string(scope), func(r chi.Router) {
						r.Post("/install", system.InstallHandler(m.config.SQL, scope, runTx))
						r.Post("/upgrade", system.UpgradeHandler(m.config.SQL, scope, runTx))
						r.Post("/downgrade", system.DowngradeHandler(m.config.SQL, scope, runTx))
						r.Post("/uninstall", system.UninstallHandler()) // no scope — no-op for both
					})
				}
			})
		})

		// MCP surface. Internal-scope only — the platform aggregates per-module
		// MCP endpoints into a single agent-facing MCP server and never exposes
		// modules directly. 1 MB cap is defense-in-depth; tool args stay small.
		r.Route("/mcp", func(r chi.Router) {
			r.Use(httputil.MaxBytes(1 << 20))
			r.Use(m.internalAuth)
			r.Get("/tools/list", system.MCPToolsListHandler(m.registry))
			r.Post("/tools/call", system.MCPToolsCallHandler(m.registry))
			r.Get("/resources/list", system.MCPResourcesListHandler(m.registry))
			r.Get("/resources/read", system.MCPResourcesReadHandler(m.registry))
		})
	})
}

// ---------------------------------------------------------------------------
// Convenience API
// ---------------------------------------------------------------------------

var defaultModule *Module

func mustDefault(caller string) *Module {
	if defaultModule == nil {
		panic("mirrorstack: must call Init() before " + caller + "()")
	}
	return defaultModule
}

// Init creates the default module.
//
//	ms.Init(ms.Config{ID: "media", Name: "Media", Icon: "perm_media"})
//	ms.Platform(func(r chi.Router) { ... })
//	ms.Start()
func Init(cfg Config) error {
	mod, err := New(cfg)
	if err != nil {
		return err
	}
	defaultModule = mod
	return nil
}

// Start starts the default module.
func Start() error {
	return mustDefault("Start").Start()
}

// DB returns a scoped database connection on the default module.
func DB(ctx context.Context) (db.Querier, func(), error) {
	return mustDefault("DB").DB(ctx)
}

// Tx runs fn inside a transaction on the default module.
func Tx(ctx context.Context, fn func(q db.Querier) error) error {
	return mustDefault("Tx").Tx(ctx, fn)
}

// ModuleDB returns a connection scoped to the module's shared schema on the
// default module.
func ModuleDB(ctx context.Context) (db.Querier, func(), error) {
	return mustDefault("ModuleDB").ModuleDB(ctx)
}

// ModuleTx runs fn inside a transaction scoped to the module's shared schema
// on the default module.
func ModuleTx(ctx context.Context, fn func(q db.Querier) error) error {
	return mustDefault("ModuleTx").ModuleTx(ctx, fn)
}

// Cache returns a scoped cache client on the default module.
func Cache(ctx context.Context) (cache.Cacher, func(), error) {
	return mustDefault("Cache").Cache(ctx)
}

// Storage returns a scoped storage client on the default module.
func Storage(ctx context.Context) (storage.Storer, error) {
	return mustDefault("Storage").Storage(ctx)
}

// Meter returns a scoped meter for recording usage events on the default module.
// Panics before Init.
func Meter(ctx context.Context) meter.Meter {
	return mustDefault("Meter").Meter(ctx)
}

// Platform registers platform-scoped routes on the default module.
func Platform(fn func(r chi.Router)) { mustDefault("Platform").Platform(fn) }

// Public registers public-scoped routes on the default module.
func Public(fn func(r chi.Router)) { mustDefault("Public").Public(fn) }

// Internal registers internal-scoped routes on the default module.
func Internal(fn func(r chi.Router)) { mustDefault("Internal").Internal(fn) }

// DefaultModule returns the default module for advanced use cases.
func DefaultModule() *Module { return defaultModule }
