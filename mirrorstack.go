// Package mirrorstack is the Go SDK for building modules on MirrorStack.
//
// Use Init() + Start() for the convenience API, or New() for testing and advanced use.
package mirrorstack

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"sync"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/go-chi/chi/v5"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/cache"
	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/runtime"
	"github.com/mirrorstack-ai/app-module-sdk/system"
)

// Config holds the module identity. Passed to Init() or New().
type Config struct {
	ID   string // Unique module identifier (required)
	Name string // Default display name (platform can override)
	Icon string // Default Material icon name (platform can override)
}

// Module is the core SDK instance.
type Module struct {
	config    Config
	router    *chi.Mux
	logger    *log.Logger
	poolCache *db.PoolCache  // production: per-app DB pools
	devDBOnce sync.Once      // dev mode: lazy DB init
	devDB     *db.DB
	devDBErr  error
	devCacheOnce sync.Once     // dev mode: lazy cache init
	devCache     *cache.Client
	devCacheErr  error
	prodCache    *cache.Client // production: from injected credential
	prodCacheMu  sync.Mutex
}

// New creates a new Module.
func New(cfg Config) (*Module, error) {
	if cfg.ID == "" {
		return nil, errors.New("mirrorstack: Config.ID is required")
	}
	m := &Module{
		config:    cfg,
		router:    chi.NewRouter(),
		logger:    log.New(os.Stderr, "mirrorstack: ", log.LstdFlags),
		poolCache: db.NewPoolCache(),
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
	pool, err := m.resolvePool(ctx)
	if err != nil {
		return nil, nil, err
	}
	return db.AcquireScoped(ctx, pool)
}

// Tx runs fn inside a transaction with schema isolation. Commits on success, rolls back on error.
//
//	err := mod.Tx(r.Context(), func(q db.Querier) error {
//	    queries := generated.New(q)
//	    item, err := queries.GetItem(ctx, id)
//	    if err != nil { return err }
//	    return queries.DeductBalance(ctx, params)
//	})
func (m *Module) Tx(ctx context.Context, fn func(q db.Querier) error) error {
	pool, err := m.resolvePool(ctx)
	if err != nil {
		return err
	}
	return db.Tx(ctx, pool, fn)
}

// resolvePool returns the right pool: per-app credential pool (production)
// or dev-mode pool (DATABASE_URL). Thread-safe via sync.Once for dev init.
func (m *Module) resolvePool(ctx context.Context) (*pgxpool.Pool, error) {
	// Production: credential injected per invocation
	if cred := db.CredentialFrom(ctx); cred != nil {
		return m.poolCache.Get(ctx, *cred)
	}

	// Dev mode: single pool from DATABASE_URL, init once
	m.devDBOnce.Do(func() {
		m.devDB, m.devDBErr = db.Open(context.Background())
	})
	if m.devDBErr != nil {
		return nil, m.devDBErr
	}
	return m.devDB.Pool(), nil
}

// Cache returns a scoped cache client. Keys are auto-prefixed with {appID}:{moduleID}:.
//
//	c, err := mod.Cache(r.Context())
//	if err != nil { ... }
//	c.Set("views:123", "42", 5*time.Minute)
//	val, err := c.Get("views:123")
func (m *Module) Cache(ctx context.Context) (cache.Cacher, error) {
	client, err := m.resolveCache(ctx)
	if err != nil {
		return nil, err
	}
	// Apply prefix from auth context
	appID := ""
	if a := auth.Get(ctx); a != nil {
		appID = a.AppID
	}
	if appID != "" {
		return client.ForApp(appID, m.config.ID), nil
	}
	return client, nil
}

func (m *Module) resolveCache(ctx context.Context) (*cache.Client, error) {
	// Production: credential from Lambda payload
	if cred := cache.CredentialFrom(ctx); cred != nil {
		m.prodCacheMu.Lock()
		defer m.prodCacheMu.Unlock()
		if m.prodCache == nil {
			c, err := cache.NewFromCredential(context.Background(), *cred)
			if err != nil {
				return nil, err
			}
			m.prodCache = c
		}
		return m.prodCache, nil
	}
	// Dev: REDIS_URL env var
	m.devCacheOnce.Do(func() {
		m.devCache, m.devCacheErr = cache.Open(context.Background())
	})
	return m.devCache, m.devCacheErr
}

// Platform registers routes with platform auth scope.
// Default: admin only. Use auth.RequirePermission for member/viewer access.
func (m *Module) Platform(fn func(r chi.Router)) {
	m.router.Group(func(r chi.Router) {
		r.Use(auth.PlatformAuth())
		fn(r)
	})
}

// Public registers routes with public auth scope (anyone, including anonymous).
func (m *Module) Public(fn func(r chi.Router)) {
	m.router.Group(fn)
}

// Internal registers routes with internal auth scope (platform-to-module only).
// Validates X-MS-Internal-Secret via constant-time comparison.
func (m *Module) Internal(fn func(r chi.Router)) {
	m.router.Group(func(r chi.Router) {
		r.Use(auth.InternalAuth())
		fn(r)
	})
}

// RequirePermission returns chi middleware that checks AppRole against allowed roles.
// Auto-registers the permission for manifest generation.
//
//	r.With(ms.RequirePermission("media.view", "admin", "member", "viewer")).Get("/items", listItems)
func RequirePermission(name string, roles ...string) func(http.Handler) http.Handler {
	return auth.RequirePermission(name, roles...)
}

// Start auto-detects Lambda vs HTTP and starts serving.
func (m *Module) Start() error {
	if runtime.IsLambda() {
		handler := runtime.NewLambdaHandler(m.router)
		lambda.Start(handler)
		return nil
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

// Close cleans up resources.
func (m *Module) Close() {
	if m.poolCache != nil {
		m.poolCache.Close()
	}
	if m.devDB != nil {
		m.devDB.Close()
	}
	if m.prodCache != nil {
		m.prodCache.Close()
	}
	if m.devCache != nil {
		m.devCache.Close()
	}
}

func (m *Module) mountSystemRoutes() {
	m.router.Route("/__mirrorstack", func(r chi.Router) {
		r.Get("/health", system.Health) // intentionally public — no auth
		r.Route("/platform", func(r chi.Router) {
			r.Use(auth.InternalAuth())
			// manifest, lifecycle — mounted by future issues
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

// Cache returns a scoped cache client on the default module.
func Cache(ctx context.Context) (cache.Cacher, error) {
	return mustDefault("Cache").Cache(ctx)
}

// Platform registers platform-scoped routes on the default module.
func Platform(fn func(r chi.Router)) { mustDefault("Platform").Platform(fn) }

// Public registers public-scoped routes on the default module.
func Public(fn func(r chi.Router)) { mustDefault("Public").Public(fn) }

// Internal registers internal-scoped routes on the default module.
func Internal(fn func(r chi.Router)) { mustDefault("Internal").Internal(fn) }

// DefaultModule returns the default module for advanced use cases.
func DefaultModule() *Module { return defaultModule }
