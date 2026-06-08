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
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/cache"
	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/contributions"
	"github.com/mirrorstack-ai/app-module-sdk/internal/core"
	"github.com/mirrorstack-ai/app-module-sdk/meter"
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

// PermissionOpts configures a permission declared via RegisterPermission:
// its default role, extra custom roles, and optional i18n Label/Description.
type PermissionOpts = core.PermissionOpts

// Label is a deferred-resolution display string built from Text (literal) or
// T (i18n catalog key). Pass it as PermissionOpts.Label / .Description.
type Label = core.Label

// Text returns a literal Label resolving to the default locale.
func Text(s string) Label { return core.Text(s) }

// T returns a catalog-key Label, resolved per loaded locale at manifest build
// from catalogs loaded via RegisterMessages.
func T(key string) Label { return core.T(key) }

// RegisterMessages loads the module's i18n catalogs from fsys/dir (one
// <locale>.json per file, nested JSON flattened to dotted keys). Call once at
// startup, typically with a //go:embed of i18n/*.json. These catalogs back the
// Labels resolved by RegisterPermission and surfaced in the manifest.
//
//	//go:embed i18n/*.json
//	var i18nFS embed.FS
//	ms.RegisterMessages(i18nFS, "i18n")
func RegisterMessages(fsys fs.FS, dir string) error { return core.RegisterMessages(fsys, dir) }

// RegisterPermission declares a module permission ONCE — its default role,
// custom roles, and resolved i18n Labels/Descriptions — recording it in the
// manifest. Call at startup (typically a declare/ package) BEFORE the routes
// that gate on it. First-wins by name. Names are slug-qualified automatically.
//
//	import "github.com/mirrorstack-ai/app-module-sdk/roles"
//	ms.RegisterPermission("users.read", ms.PermissionOpts{
//	    DefaultRole: roles.Viewer(),
//	    Label:       ms.T("permissions.users.read"),
//	})
func RegisterPermission(name string, opts PermissionOpts) {
	core.RegisterPermission(name, opts)
}

// RequirePermission returns Chi middleware gating a route on a permission
// previously declared via RegisterPermission, looked up by NAME:
//
//	ms.RequirePermission("users.read")
//
// Admin always passes. If the name was never RegisterPermission'd it is
// registered lazily as admin-only (safe-by-default) with a dev warning.
func RequirePermission(name string) func(http.Handler) http.Handler {
	return core.RequirePermission(name)
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

// --- Inter-module calls ---

// Call makes one server-mediated module-to-module hop through the platform
// dispatch, scoped to the current app (app id read from ctx via the SDK's
// auth identity — callers don't pass it). It JSON-marshals body, sends
// method to the target module's path, and decodes the JSON response into
// out (pass nil body for GET, nil out to ignore the response). path must
// include its leading slash and any raw query, e.g. "/internal/users?limit=10".
//
// The caller never holds the callee's credentials: dispatch injects the
// TARGET module's per-session token + identity before forwarding. Declare the
// target as a dependency (ms.DependsOn) — inter-module calls without a declared
// dep are a wiring bug the platform enforces.
//
// Dev/dispatch transport today; prod catalog/Lambda endpoint resolution is the
// documented #146 seam (see core.resolveCallURL).
func Call(ctx context.Context, targetModuleID, method, path string, body, out any) error {
	return core.Call(ctx, targetModuleID, method, path, body, out)
}

// CallGet is Call specialized to GET (no request body) on the default module.
func CallGet(ctx context.Context, targetModuleID, path string, out any) error {
	return core.CallGet(ctx, targetModuleID, path, out)
}

// CallPost is Call specialized to POST with a JSON body on the default module.
func CallPost(ctx context.Context, targetModuleID, path string, body, out any) error {
	return core.CallPost(ctx, targetModuleID, path, body, out)
}

// WithAppID returns a context whose inter-module Call scope is the given app,
// overriding the ambient identity's app. ms.Call reads the app id from the
// context (auth.Get) — for a handler that is the request's authenticated app,
// so authenticated callers need nothing extra. PUBLIC/proxy flows have no
// ambient identity (e.g. a sign-in proxy on ms.Public routes, where the target
// app arrives as request data, not as the caller's identity), so they set it
// explicitly:
//
//	ctx = ms.WithAppID(ctx, appID)
//	ms.CallGet(ctx, providerModuleID, "/internal/authorize-url?"+q, &out)
//
// Any existing UserID/AppRole on the context is preserved; only AppID changes.
func WithAppID(ctx context.Context, appID string) context.Context {
	var id auth.Identity
	if cur := auth.Get(ctx); cur != nil {
		id = *cur
	}
	id.AppID = appID
	return auth.Set(ctx, id)
}

// --- Dependency declarations ---

// Need is the configuration handle passed to DependsOn /
// OptionalDependOn callbacks. Use n.Table(name) to declare a SELECT
// request, n.Event(name) to declare an event subscription.
type Need = core.Need

// OnEventOption configures an OnEvent registration.
type OnEventOption = core.OnEventOption

// DependsOn declares a REQUIRED dependency on the default module. The
// optional configure callback names what the consumer wants from the
// dep — relations (n.Table) and events (n.Event). See the core package
// for the full spec syntax (id or id@constraint).
func DependsOn(spec string, configure ...func(*Need)) {
	core.DependsOn(spec, configure...)
}

// OptionalDependOn declares an OPTIONAL dependency and returns an
// OnEventOption to pass into ms.OnEvent. The dep is scoped to the
// handler — if the dep isn't installed, the event never fires.
func OptionalDependOn(spec string, configure ...func(*Need)) OnEventOption {
	return core.OptionalDependOn(spec, configure...)
}

// Resolve looks up a typed client registered by another module. v1 stub
// always returns (zero, false).
func Resolve[T any](id string) (T, bool) { return core.Resolve[T](id) }

// ContributesTo declares that this module pushes payload into host module's
// slot — the contributor side of Provide. Zero-runtime: it becomes
// manifest metadata and the platform (CLI in dev) performs the registration
// after app-owner approval. Pair with ms.DependsOn(host). See core.ContributesTo.
func ContributesTo(host, slot string, payload any) { core.ContributesTo(host, slot, payload) }

// --- UI surface ---

// ModuleUI is the module's declared UI surface. Pass to ms.RegisterUI.
type ModuleUI = core.ModuleUI

// UIComponent declares one agent-visible React component shipped by the
// module's web bundle. See ms.ModuleUI for the full shape.
type UIComponent = core.UIComponent

// UIProp is one prop declared on a UIComponent. Type is one of "text",
// "secret", "textarea", "bool", "number", "text-list".
type UIProp = core.UIProp

// UIPage is one entry in DefaultPages — a module-shipped React page
// mounted by the platform under /apps/<app-slug>/<module-slug>/<route>
// (or /apps/<app-slug>/settings/module/<module-slug>/<route> when
// Surface is UISurfaceSettings).
type UIPage = core.UIPage

// Known UIPage.Surface values. Empty (UISurfaceMain) is the default —
// pages mount at /apps/<app>/<module-slug>/<route>. UISurfaceSettings
// mounts pages at /apps/<app>/settings/module/<module-slug>/<route>
// instead, for per-module configuration UIs.
const (
	UISurfaceMain     = core.UISurfaceMain
	UISurfaceSettings = core.UISurfaceSettings
)

// RegisterUI declares the module's UI surface (agent-visible Components
// plus DefaultPages). Panics on programmer errors (duplicate names,
// invalid slug, unknown prop type). Last-write-wins.
func RegisterUI(ui ModuleUI) { core.RegisterUI(ui) }

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
// Optional cross-module deps can be co-located via OptionalDependOn:
//
//	ms.OnEvent("@anna/billing/payment", onPayment,
//	    ms.OptionalDependOn("@anna/billing@^1", func(n *ms.Need) {
//	        n.Table("invoices")
//	    }))
func OnEvent(name string, handler http.HandlerFunc, opts ...OnEventOption) {
	core.OnEvent(name, handler, opts...)
}

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

// --- Contribution slots ---

// ContributionSlot is the manifest projection of a declared slot.
type ContributionSlot = contributions.SlotInfo

// Provide declares an extension slot others contribute to on the
// default module. The type parameter T fixes the payload shape — incoming
// register requests must unmarshal cleanly into T. The SDK
// auto-mounts:
//
//	POST   /__mirrorstack/contrib/<key>/<id>   register/upsert
//	DELETE /__mirrorstack/contrib/<key>/<id>   unregister
//	GET    /__mirrorstack/contrib/<key>        list all registered
//
// Each is Internal-scoped (HMAC-gated by the SDK). Host modules that
// want to expose a Platform-scoped read with permission gating wrap
// these endpoints in their own handler — see oauth-core's
// /platform/providers for the pattern.
//
// Panics on duplicate key or before Init — matches RegisterUI /
// RequirePermission startup-error conventions.
//
//	type ProviderContribution struct {
//	    Name         string `json:"name"`
//	    Icon         string `json:"icon"`
//	    LoginPath    string `json:"login_path"`
//	    CallbackPath string `json:"callback_path"`
//	}
//
//	ms.Provide[ProviderContribution]("providers")
func Provide[T any](key string) {
	core.Provide[T](key)
}

// Contributions returns every contribution slot declared on the
// default module. Useful for tests and for hosts that want to expose
// a /platform/* read endpoint listing what they accept.
func Contributions() []ContributionSlot { return core.Contributions() }

// Contribution is a stored contribution row: ID is the contributing module's
// id (the registration key), Payload is its JSON contribution.
type Contribution = contributions.Contribution

// StoredContributions returns the contributions other modules have registered
// into this module's slot (the host-read side of Provide), newest
// first. The platform's install-time auto-register writes here; a host consumes
// them at runtime (e.g. oauth-core listing registered auth providers). See
// core.StoredContributions.
func StoredContributions(ctx context.Context, slot string) ([]Contribution, error) {
	return core.StoredContributions(ctx, slot)
}
