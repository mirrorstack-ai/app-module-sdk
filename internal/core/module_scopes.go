// Package mirrorstack is the Go SDK for building modules on MirrorStack.
//
// Use Init() + Start() for the convenience API, or New() for testing and advanced use.
package core

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/mirrorstack-ai/app-module-sdk/internal/actor"
	"github.com/mirrorstack-ai/app-module-sdk/internal/httputil"
	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
)

// This file holds the three route scopes and the middleware chain each one mounts.
//
// Split out of a single 1069-line module.go — the SDK's own version of the
// catch-all this codebase tells module authors not to write.

// DB/Tx/ModuleDB/ModuleTx, Cache/Storage/Meter, DependsOn/OptionalDependOn,
// MCPTool/MCPResource: see db.go, resources.go, describe.go, and mcp.go.

// Platform registers routes with platform auth scope. All routes
// declared inside fn are auto-mounted under /platform/ — write
// `r.Get("/users", ...)` and the route lands at /platform/users.
// Default role gate: admin only. Use Module.RequirePermission to
// open routes to member/viewer.
func (m *Module) Platform(fn func(r chi.Router)) {
	// platformActorSurface installs the private capability before proxyGuard
	// captures a pending actor assertion. PlatformAuth can activate delegation
	// only while this marker is present, so a module cannot reproduce the
	// capability by nesting the exported middleware on another surface.
	m.scopedRoutes(registry.ScopePlatform, fn, platformActorSurface, m.proxyGuard, m.platformAuth)
}

func platformActorSurface(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(actor.WithPlatformSurface(r.Context())))
	})
}

// Public registers routes with public auth scope (anyone, including
// anonymous). Auto-mounted under /public/ — write `r.Get("/me", ...)`
// and the route lands at /public/me.
func (m *Module) Public(fn func(r chi.Router)) {
	// proxyGuard is the ONLY thing standing between a direct caller and the
	// public surface trusting spoofable X-MS-App-ID / X-MS-User-ID /
	// X-MS-App-Role. Public scope has no role gate, so this is where the
	// not_proxied check has to live.
	m.scopedRoutes(registry.ScopePublic, fn, m.proxyGuard)
}

// Internal registers routes with internal auth scope (platform-to-module
// only). Auto-mounted under /internal/ — write `r.Post("/sessions", ...)`
// and the route lands at /internal/sessions. Validates
// X-MS-Internal-Secret via constant-time comparison. The middleware
// is cached on the Module at New() so OnEvent / Cron registrations reuse a
// single closure instead of constructing one per call.
func (m *Module) Internal(fn func(r chi.Router)) {
	m.scopedRoutes(registry.ScopeInternal, fn, m.internalAuth)
}

// routeBodyCap is the default body size limit for EVERY developer-registered
// route — Public, Platform and Internal alike — plus the SDK's own system
// surfaces (events, crons, tasks).
//
// 🔴 IT USED TO APPLY TO INTERNAL ONLY, which had the protection exactly
// backwards. Internal routes require a shared secret and are reachable only
// from the platform; Public routes are anonymous and are the one surface a
// stranger can post to. The capped scope was the guarded one, and the
// unguarded one was uncapped.
//
// Lambda's API Gateway caps at 6 MB, so production had a ceiling by accident.
// Dev mode has none, so an anonymous POST could stream until the module ran out
// of memory — and a module getting its first traffic in dev is the one least
// likely to have a proxy in front of it.
//
// 1 MB with no per-module override is deliberate: no module in the fleet reads
// a multipart body or a file upload on ANY route, because bulk bytes go through
// presigned storage URLs (storage.PresignGet) and never touch a module route.
// A module that genuinely needs more should reach for that, not for a bigger
// body. Adding a knob nobody needs is how the SDK grows contracts nothing
// implements.
const routeBodyCap = 1 << 20 // 1 MB

// mountSystemInternalRoute mounts an absolute-path route gated by
// internalAuth + the internal body cap, and records it in the registry
// under ScopeInternal. SDK system surfaces (events, crons, tasks) call
// here so their paths (e.g. /__mirrorstack/events/{name}) keep their
// fixed shape — user-facing Module.Internal() routes go through
// scopedRoutes instead and pick up the /internal/ prefix.
func (m *Module) mountSystemInternalRoute(method, path string, handler http.HandlerFunc) {
	m.registry.AddRoute(registry.ScopeInternal, method, path)
	m.router.With(httputil.MaxBytes(routeBodyCap), m.internalAuth).Method(method, path, handler)
}

// scopedRoutes records every route fn registers under the given scope, then
// re-attaches them to the main router with the scope's auth middleware.
//
// Routes are auto-mounted under "/<scope>/..." (e.g. /platform/users for
// Platform-scope, /internal/sessions for Internal-scope). Developers write
// just the suffix in fn — the prefix is added by sub.Route here so the
// URL contract stays in lockstep with the auth scope (no way to declare a
// Platform-scoped route at a non-/platform path).
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
func (m *Module) scopedRoutes(scope registry.Scope, fn func(r chi.Router), scopeMiddleware ...func(http.Handler) http.Handler) {
	sub := chi.NewRouter()
	// Every scope, not just Internal — see routeBodyCap for why the old
	// Internal-only rule protected the wrong surface.
	sub.Use(httputil.MaxBytes(routeBodyCap))
	for _, mw := range scopeMiddleware {
		if mw != nil {
			sub.Use(mw)
		}
	}
	sub.Route("/"+string(scope), fn)

	if err := chi.Walk(sub, func(method, route string, handler http.Handler, middlewares ...func(http.Handler) http.Handler) error {
		m.registry.AddRoute(scope, method, route)
		m.router.With(middlewares...).Method(method, route, handler)
		return nil
	}); err != nil {
		panic("mirrorstack: scopedRoutes(" + string(scope) + ") chi.Walk failed: " + err.Error())
	}
}

// Label is a deferred-resolution display string built from Text (literal) or
// T (i18n catalog key). It is an alias of i18n.Label so module code constructs
// it through the ms facade (ms.Text / ms.T) without importing the i18n package
