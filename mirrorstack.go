// Package mirrorstack is the Go SDK for building modules on MirrorStack.
//
// Use Init() + Start() for the convenience API, or New() for testing and
// advanced use. All implementation lives in internal/core; this file is a
// re-export facade — type aliases for Config and Module, and package-level
// wrapper functions that forward to the same-named symbols in core.
package mirrorstack

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/mirrorstack-ai/app-module-sdk/cache"
	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/core"
	"github.com/mirrorstack-ai/app-module-sdk/meter"
	"github.com/mirrorstack-ai/app-module-sdk/roles"
	"github.com/mirrorstack-ai/app-module-sdk/storage"
)

// Config holds the module identity. Passed to Init() or New().
type Config = core.Config

// Module is the core SDK instance. Methods on *Module live in internal/core;
// the type alias makes them callable through this package.
type Module = core.Module

// TaskHandler is invoked by the task worker for a registered background task.
type TaskHandler = core.TaskHandler

// TaskOption configures an OnTask registration.
type TaskOption = core.TaskOption

// --- Lifecycle ---

// New constructs a Module without setting the package default. Useful in tests
// or multi-module programs.
func New(cfg Config) (*Module, error) { return core.New(cfg) }

// Init creates the default Module and stores it for the package-level
// convenience wrappers to dispatch to.
func Init(cfg Config) error { return core.Init(cfg) }

// Start boots the default module: validates manifest, opens DB, binds routes,
// and starts the HTTP server or Lambda handler depending on runtime mode.
func Start() error { return core.Start() }

// DefaultModule returns the default Module created by Init, or nil if Init
// has not been called.
func DefaultModule() *Module { return core.DefaultModule() }

// --- HTTP scopes ---

// Platform registers platform-scoped routes on the default module.
func Platform(fn func(r chi.Router)) { core.Platform(fn) }

// Public registers public-scoped routes on the default module.
func Public(fn func(r chi.Router)) { core.Public(fn) }

// Internal registers internal-scoped routes on the default module.
func Internal(fn func(r chi.Router)) { core.Internal(fn) }

// RequirePermission returns Chi middleware that enforces the given roles and
// registers the permission on the manifest.
func RequirePermission(name string, allowed ...roles.Role) func(http.Handler) http.Handler {
	return core.RequirePermission(name, allowed...)
}

// --- Data ---

// DB returns a scoped database connection on the default module.
func DB(ctx context.Context) (db.Querier, func(), error) { return core.DB(ctx) }

// Tx runs fn inside a per-app transaction on the default module.
func Tx(ctx context.Context, fn func(q db.Querier) error) error { return core.Tx(ctx, fn) }

// ModuleDB returns a connection scoped to the module's shared schema.
func ModuleDB(ctx context.Context) (db.Querier, func(), error) { return core.ModuleDB(ctx) }

// ModuleTx runs fn inside a transaction scoped to the module's shared schema.
func ModuleTx(ctx context.Context, fn func(q db.Querier) error) error {
	return core.ModuleTx(ctx, fn)
}

// Cache returns a scoped cache client on the default module.
func Cache(ctx context.Context) (cache.Cacher, func(), error) { return core.Cache(ctx) }

// Storage returns a scoped storage client on the default module.
func Storage(ctx context.Context) (storage.Storer, error) { return core.Storage(ctx) }

// Meter returns a scoped meter for recording usage events on the default module.
func Meter(ctx context.Context) meter.Meter { return core.Meter(ctx) }

// --- Dependency declarations ---

// Describe sets the default module's human-readable description.
func Describe(s string) { core.Describe(s) }

// DependsOn declares a REQUIRED dependency on the default module. See the
// core package for the full spec syntax (id or id@constraint).
func DependsOn(spec string) { core.DependsOn(spec) }

// Needs declares an OPTIONAL dependency and returns the handler unchanged.
func Needs(spec string, h http.HandlerFunc) http.HandlerFunc { return core.Needs(spec, h) }

// Resolve looks up a typed client registered by another module. v1 stub
// always returns (zero, false).
func Resolve[T any](id string) (T, bool) { return core.Resolve[T](id) }

// --- Agent surface (MCP) ---

// MCPTool registers an agent-callable tool on the default module with JSON
// Schema derived from the type parameters via reflection.
func MCPTool[In, Out any](name, description string, handler func(ctx context.Context, args In) (Out, error)) {
	core.MCPTool[In, Out](name, description, handler)
}

// MCPResource registers an agent-readable resource on the default module.
func MCPResource[Out any](name, description string, handler func(ctx context.Context) (Out, error)) {
	core.MCPResource[Out](name, description, handler)
}

// --- Events / crons / tasks ---

// OnEvent subscribes the default module to an event from another module.
func OnEvent(name string, handler http.HandlerFunc) { core.OnEvent(name, handler) }

// Emits declares that the default module emits the named event.
func Emits(name string) { core.Emits(name) }

// Cron registers a scheduled job on the default module.
func Cron(name, schedule string, handler http.HandlerFunc) {
	core.Cron(name, schedule, handler)
}

// OnTask registers a background task handler on the default module.
func OnTask(name string, handler TaskHandler, opts ...TaskOption) {
	core.OnTask(name, handler, opts...)
}

// RunTask enqueues a task on the default module.
func RunTask(ctx context.Context, name string, payload json.RawMessage) (string, error) {
	return core.RunTask(ctx, name, payload)
}
