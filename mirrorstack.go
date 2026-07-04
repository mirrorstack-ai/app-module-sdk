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
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/cache"
	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/contributions"
	"github.com/mirrorstack-ai/app-module-sdk/internal/core"
	"github.com/mirrorstack-ai/app-module-sdk/internal/httputil"
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

// MetricOption configures a metric at declaration. The KIND is itself an option
// (Counter / Gauge), alongside Unit and Price.
type MetricOption = meter.MetricOption

// Counter and Gauge are the metric-kind options passed to Meter. Counter is
// additive (the platform SUMs it); Gauge is an absolute level (MAX or a
// time-weighted integral, never summed).
//
// They are CONSTANTS, not vars, so a third-party module cannot reassign
// ms.Counter / ms.Gauge (the SDK is a trust boundary). The call site stays
// parens-free: ms.Meter(name, ms.Counter, ...).
const (
	Counter = meter.Counter
	Gauge   = meter.Gauge
)

// Unit sets a metric's display unit (e.g. "order", "byte"). Optional.
func Unit(u string) MetricOption { return meter.Unit(u) }

// Price sets a metric's per-unit CUSTOMER price in micro-dollars (1e-6 USD).
// Optional — omit to meter without charging. The platform charges
// quantity × this price with NO blanket markup for a module's custom metric.
func Price(microDollars int64) MetricOption { return meter.Price(microDollars) }

// MetricLabel sets a metric's per-locale display label, built from ms.Text
// (literal) or ms.T (i18n catalog key), resolved against the module's i18n
// catalogs at manifest build (mirrors PermissionOpts.Label). Optional.
func MetricLabel(l Label) MetricOption { return meter.MetricLabel(l) }

// MetricUnitLabel sets a metric's per-locale display label for its UNIT, built
// from ms.Text (literal) or ms.T (i18n catalog key), resolved against the
// module's i18n catalogs at manifest build (mirrors MetricLabel). The Unit
// itself stays the untranslated billing identifier; this is a separate
// display-only label. Optional.
func MetricUnitLabel(l Label) MetricOption { return meter.MetricUnitLabel(l) }

// Meter DECLARES a usage metric on the default module. Call it ONCE per metric
// in startup code (exactly like ms.Emits / ms.RegisterPermission) — it registers
// the metric as a SIDE EFFECT and returns NOTHING. The declaration (kind + unit
// + price) is recorded in the manifest so the platform's metric catalog is
// authoritative before any event arrives.
//
// The kind is an OPTION: pass ms.Counter (additive; platform SUMs) or ms.Gauge
// (absolute level; platform takes MAX or a time-weighted integral). Emit at
// runtime BY NAME with ms.Record(ctx, name, value) — mirroring ms.Emits/ms.Emit;
// the platform reads the declared kind from the catalog, so a call site can
// never mislabel a metric.
//
// A custom metric MUST pass exactly one kind option. A reserved
// infra.*/platform.* metric is platform-measured: it may carry ms.Price ONLY
// (to override the customer passthrough) — passing a kind or unit on it panics,
// as does a duplicate name, an invalid name, or conflicting kinds.
//
//	ms.Meter("orders.placed", ms.Counter, ms.Unit("order"), ms.Price(50_000))
//	ms.Meter("infra.compute.ms", ms.Price(0)) // absorb platform compute
func Meter(name string, opts ...MetricOption) {
	core.Meter(name, opts...)
}

// Record emits a usage event for the metric DECLARED (via ms.Meter) under name
// — BY NAME, exactly mirroring ms.Emit. The platform reads the declared kind
// from its catalog to decide how to aggregate, so the call site never repeats
// the kind.
//
// Declaration-first: Record returns an error if name was never declared via
// ms.Meter (fail fast). It also returns an error (never panics) if value is
// negative or non-finite. A billing failure must NEVER fail the handler — log
// the error, don't propagate it.
//
//	if err := ms.Record(r.Context(), "orders.placed", 1); err != nil {
//	    log.Printf("meter: %v", err) // don't fail the handler
//	}
func Record(ctx context.Context, name string, value float64) error {
	return core.Record(ctx, name, value)
}

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

// Emit publishes an event to every LIVE module that subscribes to name within
// the current app, scoped to the current app (app id read from ctx via the
// SDK's auth identity — callers don't pass it). The SDK wraps payload in the
// event envelope ({id, name, sourceModuleID, sentAt, payload}) and POSTs it to
// the platform dispatch event bus, which fans the SAME envelope out to each
// subscriber session. An empty app-scope context is an error (no panic).
//
// Delivery is AT-LEAST-ONCE and best-effort per subscriber — subscriber
// handlers (ms.OnEvent) must be idempotent. Declare the event vocabulary with
// ms.Emits so subscribers can discover it.
//
// Dev/dispatch transport today; prod event-bus endpoint resolution is the
// documented #146 seam (see core.resolveEventBusURL, mirroring core.resolveCallURL).
func Emit(ctx context.Context, name string, payload any) error {
	return core.Emit(ctx, name, payload)
}

// Notification is the shape passed to Notify: an i18n Title (required) and
// Body (optional) — Labels built from ms.Text / ms.T, resolved to per-locale
// maps at send time so the platform picks the recipient's locale — plus an
// optional Icon and in-app Link, and the target Audience (defaults to
// NotifyAdmins when unset).
type Notification = core.Notification

// NotifyAudience selects WHO inside the app receives a notification:
// NotifyAdmins (the default) or NotifyAllMembers.
type NotifyAudience = core.NotifyAudience

// NotifyAdmins and NotifyAllMembers are the notification audiences. They are
// CONSTANTS, not vars, so a third-party module cannot reassign them (the SDK
// is a trust boundary).
const (
	NotifyAdmins     = core.NotifyAdmins
	NotifyAllMembers = core.NotifyAllMembers
)

// Notify sends an in-app notification to the current app's members, scoped to
// the current app (app id read from ctx via the SDK's auth identity — callers
// don't pass it). The SDK resolves the Notification's Labels, wraps them in
// the notification envelope ({id, sentAt, sourceModuleID, title, body, icon,
// link, audience}) and POSTs it to the platform dispatch notification ingress,
// which re-derives the sender identity from the live session and writes the
// notification into the app's feed. An empty app-scope context, an unset
// Title, or an unknown Audience is an error (no panic).
//
//	ms.Notify(r.Context(), ms.Notification{
//	    Title:    ms.T("notifications.order.placed"),
//	    Link:     "/orders/42",
//	    Audience: ms.NotifyAllMembers,
//	})
//
// Dev/dispatch transport today; prod notification-ingress resolution is the
// documented #146 seam (see core.resolveNotifyURL, mirroring core.resolveEventBusURL).
func Notify(ctx context.Context, n Notification) error {
	return core.Notify(ctx, n)
}

// AppID returns the current app id from the request context, or "" if no
// identity is set. It is the inbound twin of WithAppID and the single
// unspoofable way a handler reads its OWN app.
//
// On every guarded surface the SDK promotes the platform's trusted, dispatch-
// injected app id into the context identity BEFORE the handler runs — Platform
// via PlatformAuth, Public via the proxy guard's validated-token path, and
// Lambda via the typed invoke payload (runtime.InjectResources). So a Public or
// Platform handler reads its app with:
//
//	appID := ms.AppID(r.Context())
//
// Do NOT read the app id from request data (query string, body, path) — those
// are caller-controlled and forgeable. And do NOT read it from the
// auth.HeaderAppID header: the deployed Lambda shim STRIPS every client-
// settable X-MS-* identity header before the router runs (identity rides the
// typed invoke payload instead), so a header read works under the dev tunnel
// and silently breaks deployed — the exact bug shipped in ms-app-modules#30.
// ms.AppID is the trusted source on every path.
func AppID(ctx context.Context) string {
	return core.AppID(ctx)
}

// UserID returns the current user id from the request context, or "" if no
// identity is set. Together with ms.AppID and ms.AppRole (or auth.Get, which
// returns the full Identity in one read) it is the ONLY correct way for
// module code to read the request's identity: always the context, never the
// X-MS-* headers.
//
// On every guarded surface the SDK promotes the platform's trusted, dispatch-
// injected user id into the context identity BEFORE the handler runs — Platform
// via PlatformAuth, Public via the proxy guard's validated-token path, and
// Lambda via the typed invoke payload (runtime.InjectResources). So a handler
// reads the requesting user with:
//
//	userID := ms.UserID(r.Context())
//
// Do NOT read the auth.HeaderUserID (X-MS-User-ID) header instead: the
// deployed Lambda shim STRIPS every client-settable X-MS-* identity header
// before the router runs, so a header read works under the dev tunnel and
// silently breaks deployed (empty value / rejected request) — the exact bug
// class shipped in ms-app-modules#30. Those headers are the platform-to-SDK
// wire, not a module identity API.
//
// "" is a legitimate value, not only an unauthenticated-middleware artifact:
// internal/system/cron/task invocations carry no user, and an anonymous Public
// request may carry an identity whose user id is empty. Under the local-dev
// bypass (no secret configured) it returns the synthetic "local-dev-user".
func UserID(ctx context.Context) string {
	return core.UserID(ctx)
}

// AppRole returns the current user's role in the app from the request context
// ("admin", "member", or "viewer" — compare against auth.RoleAdmin /
// auth.RoleMember / auth.RoleViewer), or "" if no identity is set. Together
// with ms.AppID and ms.UserID (or auth.Get, which returns the full Identity
// in one read) it is the ONLY correct way for module code to read the
// request's identity: always the context, never the X-MS-* headers. Note:
// ms.AppRole is a read of WHO the platform says the caller is — for gating
// routes by role, prefer the declarative RequirePermission middleware.
//
// On every guarded surface the SDK promotes the platform's trusted, dispatch-
// injected role into the context identity BEFORE the handler runs — Platform
// via PlatformAuth, Public via the proxy guard's validated-token path, and
// Lambda via the typed invoke payload (runtime.InjectResources). So a handler
// reads the caller's role with:
//
//	if ms.AppRole(r.Context()) == auth.RoleAdmin { ... }
//
// Do NOT read the auth.HeaderAppRole (X-MS-App-Role) header instead: the
// deployed Lambda shim STRIPS every client-settable X-MS-* identity header
// before the router runs, so a header read works under the dev tunnel and
// silently breaks deployed (empty value / rejected request) — the exact bug
// class shipped in ms-app-modules#30. Those headers are the platform-to-SDK
// wire, not a module identity API.
//
// "" is a legitimate value: internal/system/cron/task invocations carry no
// user role. Under the local-dev bypass (no secret configured) it returns the
// synthetic auth.RoleAdmin.
func AppRole(ctx context.Context) string {
	return core.AppRole(ctx)
}

// Log returns the request's structured logger, pre-tagged with the trusted
// app_id / request_id / module_id correlation fields so every line is
// attributable in the platform Logcat. Prefer it over the standard library
// `log` for all module logging. Outside a request it returns the process
// default logger.
//
//	ms.Log(r.Context()).Info("user signed in", "provider", "google")
func Log(ctx context.Context) *slog.Logger {
	return core.LoggerFrom(ctx)
}

// WithAppID returns a context whose inter-module Call scope is the given app,
// overriding the ambient identity's app. ms.Call reads the app id from the
// context (auth.Get) — for a handler that is the request's authenticated app,
// so authenticated callers need nothing extra.
//
// Use it to RETARGET an outbound ms.Call at a DIFFERENT app than the ambient
// one (the rare cross-app proxy case). To read your OWN app id, use ms.AppID —
// the SDK already promotes the request's trusted app into the identity, so a
// Public/Platform handler does not need WithAppID just to call within its own
// app:
//
//	ctx = ms.WithAppID(ctx, otherAppID)
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

// --- HTTP responses ---

// ErrorResponse is the SDK's JSON error envelope: {"error": "<message>"}. It is
// the shape WriteError emits and the same shape the SDK's own auth/permission
// middleware returns, so module errors and platform errors are indistinguishable
// on the wire. Exported so callers can decode an error body into a typed value.
type ErrorResponse = httputil.ErrorResponse

// WriteJSON writes v to w as JSON with the given status code and a
// Content-Type: application/json header. A nil v writes only the status and
// header with no body (for 204-style empty responses). An encode error is
// logged, not returned — the status line is already committed by then.
//
// This is the response writer every module otherwise hand-rolls; it backs the
// same internal helper the SDK uses for its own endpoints.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	if v == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		return
	}
	httputil.JSON(w, status, v)
}

// WriteError writes the JSON error envelope {"error": msg} with the given
// status code, via WriteJSON. Use the canonical ErrorResponse shape rather than
// an ad-hoc map so module errors match the SDK's own error wire format.
func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, ErrorResponse{Error: msg})
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

// ExposeTable marks a table in this module's schema as read-only
// SELECT-eligible for a depending module — the producer side of DependsOn's
// n.Table. It is a zero-runtime DECLARATION that lands in the manifest under
// exposes.tables; the platform issues GRANT SELECT after the app owner
// approves a dependency.
//
// The producer only opts a table IN to being readable — it does NOT decide
// WHO reads it. The app owner (not the producer) is the trust root and
// chooses which installed modules may read by approving their dependency.
// There is intentionally NO consumer allowlist: in a marketplace the
// consumers are third parties, so a publisher-controlled reader list is the
// wrong model. v1 is TABLES ONLY, read-only (SELECT). Panics on an empty or
// invalid table identifier. Call from startup code.
func ExposeTable(name string) { core.ExposeTable(name) }

// --- Dependency reads (exposed tables, via the platform read proxy) ---

// Dependency is the read handle DependencyDB returns — the consumer side of
// ExposeTable at runtime. It builds STRUCTURED reads only (never raw SQL,
// never a pool): Select(table).Columns(...).Where(...).WhereIn(...).Limit(n).
type Dependency = core.Dependency

// DependencyQuery is the structured read builder started by
// Dependency.Select. Builder methods validate eagerly and latch the first
// error; Rows/Result surface it without touching the network.
type DependencyQuery = core.DependencyQuery

// DependencyResult is one executed dependency read: decoded rows (never nil)
// plus whether the read was cut at the limit (more rows exist).
type DependencyResult = core.DependencyResult

// Typed failure modes for dependency reads — match with errors.Is. All
// fail-closed: a dependency read NEVER silently returns empty rows for an
// authorization or availability failure.
var (
	// ErrDependencyUnauthorized — the platform could not authenticate the
	// read (no live dev-tunnel session / wrong service secret / module not
	// in the app). Re-establish the tunnel session.
	ErrDependencyUnauthorized = core.ErrDependencyUnauthorized
	// ErrNotExposed — the table is not exposed to this module by the version
	// the producer actually runs, or the app owner never consented.
	ErrNotExposed = core.ErrNotExposed
	// ErrDependencyUnavailable — authorized, but the producer's relation is
	// not readable right now (yanked/rolled back), or the platform's read
	// proxy is disabled.
	ErrDependencyUnavailable = core.ErrDependencyUnavailable
	// ErrProducerNotFound — the producer ref does not resolve to an install
	// in this app.
	ErrProducerNotFound = core.ErrProducerNotFound
)

// DependencyDB returns a read-only handle on a producer module's exposed
// tables within the current app — the runtime counterpart of
// ms.DependsOn(..., n.Table(...)). producerRef takes the same forms as
// DependsOn specs: "@owner/slug", bare "slug", the m<hex> module ID, or a
// dashed UUID (a trailing @<constraint> is ignored — reads target the
// version the producer actually runs). The app scope is read from ctx via
// the SDK's auth identity.
//
//	rows, err := ms.DependencyDB(ctx, "@owner/oauth-core").
//	    Select("users").
//	    Columns("id", "email").
//	    WhereIn("id", 1, 2, 3).
//	    Rows(ctx)
//
// The read executes on the PLATFORM as this module's own per-app DB role in
// a READ ONLY transaction, authorized against the consent+exposure catalog.
// There is no cross-plane SQL JOIN — fetch the exposed rows here, read your
// own tables via mod.DB, and join in application code.
//
// Dev-plane (`mirrorstack dev --tunnel`) only today; a deployed module reads
// a co-located producer directly via mod.DB (GRANT SELECT). Panics before
// Init — matching Platform/Public/Internal.
func DependencyDB(ctx context.Context, producerRef string) *Dependency {
	return core.DependencyDB(ctx, producerRef)
}

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

// MCPToolOption configures an MCPTool declaration (e.g. ToolPermission).
type MCPToolOption = core.MCPToolOption

// ToolPermission gates an MCP tool on a module permission (SHORT name,
// slug-qualified exactly like RegisterPermission). The platform lists and
// invokes the tool only for callers whose effective permissions include it.
// An undeclared name registers lazily as admin-only (fail closed).
//
//	ms.RegisterPermission("users.read", ms.PermissionOpts{DefaultRole: roles.Viewer()})
//	ms.MCPTool("list-users", "List users", listUsers, ms.ToolPermission("users.read"))
func ToolPermission(name string) MCPToolOption { return core.ToolPermission(name) }

// MCPTool registers an agent-callable tool on the default module with JSON
// Schema derived from the type parameters via reflection. Optional
// MCPToolOptions scope the tool, e.g. ms.ToolPermission("users.read").
func MCPTool[In, Out any](name, description string, handler func(ctx context.Context, args In) (Out, error), opts ...MCPToolOption) {
	core.MCPTool[In, Out](name, description, handler, opts...)
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
