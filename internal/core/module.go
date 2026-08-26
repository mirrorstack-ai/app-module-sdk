// Package mirrorstack is the Go SDK for building modules on MirrorStack.
//
// Use Init() + Start() for the convenience API, or New() for testing and advanced use.
package core

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/cache"
	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/contributions"
	"github.com/mirrorstack-ai/app-module-sdk/internal/ids"
	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
	"github.com/mirrorstack-ai/app-module-sdk/meter"
)

// This file holds construction: New() and the accessors every other file reaches through.
//
// Split out of a single 1069-line module.go — the SDK's own version of the
// catch-all this codebase tells module authors not to write.

func New(cfg Config) (*Module, error) {
	if cfg.ID == "" {
		return nil, errors.New("mirrorstack: Config.ID is required")
	}
	// Generic managed runners receive the broker's canonical catalog UUID,
	// while module schemas and manifests use the identifier-safe m<32hex>
	// representation. Normalize at the construction boundary so every internal
	// consumer sees one stable form. Legacy human-readable dev IDs remain valid.
	if normalized, ok := ids.NormalizeModuleID(cfg.ID); ok {
		cfg.ID = normalized
	}
	if !moduleIDPattern.MatchString(cfg.ID) {
		return nil, fmt.Errorf("mirrorstack: Config.ID %q must match %s (lowercase, starts with letter, max 36 chars)", cfg.ID, moduleIDPattern)
	}
	if cfg.Slug != "" && !moduleSlugPattern.MatchString(cfg.Slug) {
		return nil, fmt.Errorf("mirrorstack: Config.Slug %q must match %s (lowercase, starts with letter, hyphens allowed, max 16 chars)", cfg.Slug, moduleSlugPattern)
	}
	if err := validateClientSpec(cfg.Client); err != nil {
		return nil, err
	}
	m := &Module{
		config:         cfg,
		router:         chi.NewRouter(),
		logger:         log.New(os.Stderr, "mirrorstack: ", log.LstdFlags),
		registry:       registry.New(),
		contribReg:     contributions.NewRegistry(),
		contribStorage: contributions.NewStorage(cfg.ID),
		internalAuth:   auth.InternalAuth(),
		proxyGuard:     auth.RequireProxy(),
		platformAuth:   auth.PlatformAuth(),
		poolCache:      db.NewPoolCache(),
		cacheCache:     cache.NewClientCache(),
		taskHandlers:   make(map[string]taskEntry),
		localTasks:     newLocalTaskRegistry(),
		taskHTTPClient: &http.Client{Timeout: 15 * time.Second},
	}

	// Local-dev CORS: when NO platform-secret source is configured (i.e. the
	// auth/PlatformAuth + InternalAuth + proxy-guard bypass branch is active),
	// echo the request Origin so a module's React bundle running on the
	// platform's domain (e.g. localhost:3001) can fetch the module's
	// own /platform/* and /me endpoints cross-origin. Wildcard "*"
	// won't do — the bundle's api.ts uses credentials: 'include' for
	// the /me cookie flow, and browsers reject "*" with credentials.
	//
	// Tunnel/prod (a secret is configured) leaves CORS off entirely — bundles
	// in those modes go through the platform proxy at same-origin, so
	// cross-origin requests from random origins shouldn't be allowed.
	//
	// Gate on the FULL secret chain (auth.SecretConfigured), not just
	// MS_INTERNAL_SECRET: with the MS_PLATFORM_TOKEN[_FILE] > MS_INTERNAL_SECRET
	// hierarchy, a tunnel module may carry only MS_PLATFORM_TOKEN. Checking the
	// old single var would attach permissive credentialed CORS in that
	// enforcing mode — reflecting arbitrary origins, the exact surface this
	// dev-only helper must never expose outside local dev.
	if !auth.SecretConfigured() {
		m.router.Use(localDevCORS)
	}

	// Dev per-app schema injection: under `mirrorstack dev` the platform's
	// Lambda shim is absent, so the SDK derives app_<id> from X-MS-App-ID and
	// provisions it lazily (see devAppSchemaMiddleware). Attached before routes
	// are mounted so it wraps every scope, including the system /__mirrorstack/*
	// surfaces (e.g. /contrib) that also operate on per-app data.
	m.devMode = m.devMigrateEnabled()
	if m.devMode {
		m.router.Use(m.devAppSchemaMiddleware)
	}

	// Structured logging: JSON to stdout (captured by CloudWatch in prod and the
	// `mirrorstack dev` runner in dev) + a per-request correlated logger (ms.Log)
	// carrying the trusted app_id/request_id/module_id. Mounted after the dev
	// schema middleware so the request's identity is already in context; runs on
	// every request, both serving paths.
	configureLogging()
	m.router.Use(m.requestLogMiddleware)

	// Meter client: dispatch-HTTP transport in both dev and prod (Record POSTs
	// each usage Event to the dispatch usage ingress, like ms.Emit). The HTTP
	// client is always built (never nil); New fail-fast validates MS_DISPATCH_URL
	// when set so a typo surfaces at startup rather than as silently lost usage.
	meterClient, err := meter.New()
	if err != nil {
		return nil, fmt.Errorf("mirrorstack: init meter client: %w", err)
	}
	m.meterClient = meterClient

	// A Config-provided description flows to the registry so it reaches the
	// manifest like Name/Tags. Skip when empty to avoid a blank override.
	if cfg.Description != "" {
		m.registry.SetDescription(cfg.Description)
	}
	// A DescriptionLabel rides ALONGSIDE the plain Description as a per-locale
	// map in the manifest (mirroring permission/metric labels). Stored opaque
	// and resolved lazily at manifest build, so catalog-load ordering is free.
	// Skip a zero Label so plain-string modules ship no descriptionLabels key.
	if !cfg.DescriptionLabel.IsZero() {
		m.registry.SetDescriptionLabel(cfg.DescriptionLabel)
	}

	// The local dependency plane's directory seams. Bound to the real
	// Postgres-backed implementations here so they are never nil in production
	// code; tests overwrite them with in-memory fakes to exercise resultLocal
	// and the boot publish ordering without a DB.
	m.devDir = newDevDirectoryState(m)

	m.mountSystemRoutes()
	return m, nil
}

func (m *Module) Config() Config   { return m.config }
func (m *Module) Router() *chi.Mux { return m.router }
