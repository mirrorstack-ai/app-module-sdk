// Package mirrorstack is the Go SDK for building modules on MirrorStack.
//
// Use Init() + Start() for the convenience API, or New() for testing and advanced use.
package core

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/contributions"
	"github.com/mirrorstack-ai/app-module-sdk/internal/httputil"
	"github.com/mirrorstack-ai/app-module-sdk/internal/migration"
	"github.com/mirrorstack-ai/app-module-sdk/internal/runtime"
	"github.com/mirrorstack-ai/app-module-sdk/system"
)

// This file holds the reserved /__mirrorstack/* namespace the platform calls.
//
// Split out of a single 1069-line module.go — the SDK's own version of the
// catch-all this codebase tells module authors not to write.

func (m *Module) mountSystemRoutes() {
	m.router.Route("/__mirrorstack", func(r chi.Router) {
		r.Get("/health", system.Health) // intentionally public — no auth

		// Dev-mode Lambda transport shim: dispatch's localHTTPInvoker POSTs
		// LambdaRequest envelopes here when MS_MODULE_LAMBDA_DEV_URL points at
		// this module (module_deploys simulation without real Lambda). Never
		// mounted in Lambda mode — the real transport owns invocation there,
		// and not mounting removes any prod re-entrancy surface. Auth is the
		// handler's own envelope-secret gate (see lambdaInvokeShim):
		// internalAuth would validate the wrong secret (the tunnel session
		// token, not the lambda secret dispatch puts INSIDE the envelope).
		if !runtime.IsLambda() {
			r.Post("/lambda-invoke", m.lambdaInvokeShim())
		}

		// Public-scope static handler for the module's React bundle (the
		// directory the author named in Config.WebDir). Browser-fetched
		// from the platform's catch-all module page; CORS-permissive
		// because bundles carry no credentials. When WebDir is empty,
		// every request 404s.
		r.Get("/web/*", system.WebHandler(m.config.WebDir))
		r.Head("/web/*", system.WebHandler(m.config.WebDir))
		r.Options("/web/*", system.WebHandler(m.config.WebDir))

		// Contribution slots — host modules declare with
		// ms.Provide and the SDK auto-mounts register /
		// unregister / list endpoints here. Internal scope because
		// writes move trusted module-to-module; the read path is
		// inside the same group for symmetry — host modules that want
		// a Platform-scoped read with permission gating add their own
		// wrapper that calls into storage directly.
		r.Route("/contrib", func(r chi.Router) {
			r.Use(httputil.MaxBytes(contributions.MaxPayloadBytes + 1024))
			r.Use(m.internalAuth)
			handlers := contributions.NewHandlers(
				m.contribReg,
				m.contribStorage,
				func(req *http.Request) (db.Querier, func(), error) { return m.DB(req.Context()) },
			)
			r.Mount("/", handlers.Routes())
		})

		r.Route("/platform", func(r chi.Router) {
			r.Use(m.internalAuth)
			// 8 MB group-wide backstop. Defense-in-depth only: every route
			// below applies its own tighter cap (64 KB via smallBody, or
			// seed's own 2 MB reader — see system/lifecycle.go's
			// seedMaxBodyBytes), and nesting http.MaxBytesReader wraps only
			// ever shrinks the effective limit to the smallest one in the
			// chain, so this never fights those. It exists so a FUTURE route
			// added here without remembering its own cap still gets bounded
			// instead of silently inheriting an unlimited body.
			r.Use(httputil.MaxBytes(8 << 20))
			// 64 KB — manifest + lifecycle install/upgrade/downgrade/uninstall
			// bodies are tiny. Applied per-route via .With() rather than
			// folded into the group backstop above so the seed route below
			// (COPY-text chunks up to 2MB) can enforce its own larger cap
			// instead of inheriting this smaller one — nesting two
			// http.MaxBytesReader wraps only ever shrinks the effective limit
			// to the smaller of the two.
			smallBody := httputil.MaxBytes(64 * 1024)
			r.With(smallBody).Get("/manifest", system.ManifestHandlerWithClient(
				m.config.ID, m.config.Slug, m.config.Name, m.config.Icon, m.config.Tags,
				m.config.SQL, m.config.Versions, m.registry, m.contribReg, m.config.Client,
			))
			r.Route("/lifecycle", func(r chi.Router) {
				// App and module migrations are separate tracks on disjoint
				// directories AND disjoint DB credentials; mount the same
				// four endpoints under each scope so the platform can drive
				// them independently.
				for _, scope := range migration.AllScopes() {
					runTx := m.lifecycleTxRunner(scope)
					// SDK-owned per-app setup (the contributions store) rides
					// install + upgrade, not downgrade: reverting an author's
					// migrations is not a reason to drop SDK infrastructure, and
					// re-creating it there would be the only DDL a downgrade did.
					provision := m.lifecycleProvisioner(scope)
					r.Route("/"+string(scope), func(r chi.Router) {
						r.With(smallBody).Post("/install", system.InstallHandler(m.config.SQL, scope, runTx, provision))
						r.With(smallBody).Post("/upgrade", system.UpgradeHandler(m.config.SQL, scope, runTx, provision))
						r.With(smallBody).Post("/downgrade", system.DowngradeHandler(m.config.SQL, scope, runTx))
						r.With(smallBody).Post("/uninstall", system.UninstallHandler()) // no scope — no-op for both

						if scope == migration.ScopeApp && !runtime.IsLambda() {
							// Dev-mount seed (F2 of the transient-overlay
							// work): api-platform's devseed.Seeder POSTs one
							// Postgres COPY-text chunk per call into this
							// app's schema. App-scope only — there is no
							// module-scope seed. SeedHandler enforces its own
							// 2MB http.MaxBytesReader (system/lifecycle.go),
							// so it deliberately does NOT get smallBody.
							//
							// !runtime.IsLambda() mirrors the lambda-invoke shim
							// gate above (mountSystemRoutes, ~line 760): SeedHandler
							// executes req.CreateSQL as raw request-body SQL, a
							// surface the rest of the lifecycle deliberately
							// withholds in prod. Dev/tunnel only.
							r.Post("/seed", system.SeedHandler(func(ctx context.Context) (system.SeedConn, func(), error) {
								return m.seedConn(ctx)
							}))
						}
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
			logFailure := func(ctx context.Context, kind, name string, err error) {
				LoggerFrom(ctx).Error("MCP handler failed", "kind", kind, "name", name, "error", err)
			}
			r.Post("/tools/call", system.MCPToolsCallHandlerWithFailureLogger(m.registry, logFailure))
			r.Get("/resources/list", system.MCPResourcesListHandler(m.registry))
			r.Get("/resources/read", system.MCPResourcesReadHandlerWithFailureLogger(m.registry, logFailure))
		})
	})
}

// ---------------------------------------------------------------------------
// Convenience API
// ---------------------------------------------------------------------------
