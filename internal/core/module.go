// Package mirrorstack is the Go SDK for building modules on MirrorStack.
//
// Use Init() + Start() for the convenience API, or New() for testing and advanced use.
package core

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/go-chi/chi/v5"

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

// DB/Tx/ModuleDB/ModuleTx, Cache/Storage/Meter, Describe/DependsOn/Needs,
// MCPTool/MCPResource: see db.go, resources.go, describe.go, and mcp.go.

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

// internalRouteBodyCap is the default body size limit for Internal-scope
// routes (events, crons, tasks, and developer-registered internal handlers).
// Defense-in-depth — Lambda's API Gateway has a 6 MB cap, but dev mode is
// unbounded without this.
const internalRouteBodyCap = 1 << 20 // 1 MB

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

// Platform registers platform-scoped routes on the default module.
func Platform(fn func(r chi.Router)) { mustDefault("Platform").Platform(fn) }

// Public registers public-scoped routes on the default module.
func Public(fn func(r chi.Router)) { mustDefault("Public").Public(fn) }

// Internal registers internal-scoped routes on the default module.
func Internal(fn func(r chi.Router)) { mustDefault("Internal").Internal(fn) }

// DefaultModule returns the default module for advanced use cases.
func DefaultModule() *Module { return defaultModule }
