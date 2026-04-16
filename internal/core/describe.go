package core

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/Masterminds/semver/v3"

	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
)

// Describe sets the module's human-readable description. Used by the catalog
// for agent discovery ("find a module that does X"). One to three sentences is
// typical. Last call wins; typically called once at module init.
//
//	ms.Describe("Google OAuth provider: authorize, callback, session issue.")
func (m *Module) Describe(s string) {
	m.registry.SetDescription(s)
}

// DependsOn declares a REQUIRED dependency on another module. The catalog
// installs the dependency before this module; install fails if the dependency
// is unavailable or no published version matches the constraint.
//
// Spec syntax: "id" (any version) or "id@constraint" where constraint is a
// SemVer range in npm-style syntax:
//
//	ms.DependsOn("oauth-core")           // any version
//	ms.DependsOn("oauth-core@^1.2.0")    // >=1.2.0, <2.0.0
//	ms.DependsOn("oauth-core@~1.2.0")    // >=1.2.0, <1.3.0
//	ms.DependsOn("oauth-core@>=1.2 <2")  // explicit range
//	ms.DependsOn("oauth-core@1.2.3")     // exact
//
// Panics at call time on an invalid constraint — it's a programmer error,
// not runtime input.
//
// For OPTIONAL deps (the module works standalone but integrates with the
// dependency when present), use ms.Needs at the handler registration site.
func (m *Module) DependsOn(spec string) {
	id, constraint := parseDepSpec(spec)
	m.registry.AddDependency(registry.Dependency{ID: id, Version: constraint, Optional: false})
}

// Needs declares an OPTIONAL dependency on another module and returns the
// handler unchanged. Designed to wrap handlers passed to ms.OnEvent, ms.Cron,
// chi routes — anywhere a handler is registered where the work may use the
// dependency.
//
// Spec syntax mirrors DependsOn: "id" or "id@constraint".
//
//	ms.OnEvent("video.completed", ms.Needs("video@^1", onVideoCompleted))
//	ms.Cron("cleanup", "0 3 * * *", ms.Needs("storage", runCleanup))
//
// Nest Needs calls to declare multiple optional deps for one handler:
//
//	ms.OnEvent("payment",
//	    ms.Needs("billing@^1", ms.Needs("audit-log", onPayment)))
//
// Dedup: if the same ID has also been declared required via ms.DependsOn
// elsewhere, required wins — an additional Needs is a no-op.
func (m *Module) Needs(spec string, h http.HandlerFunc) http.HandlerFunc {
	id, constraint := parseDepSpec(spec)
	m.registry.AddDependency(registry.Dependency{ID: id, Version: constraint, Optional: true})
	return h
}

// parseDepSpec splits "id@constraint" into its two parts. The id is required;
// the constraint is optional. Panics on an invalid SemVer constraint.
func parseDepSpec(spec string) (id, constraint string) {
	id, constraint, hasAt := strings.Cut(spec, "@")
	if !hasAt || constraint == "" {
		return id, ""
	}
	if _, err := semver.NewConstraint(constraint); err != nil {
		panic(fmt.Sprintf("mirrorstack: invalid SemVer constraint in dependency spec %q: %v", spec, err))
	}
	return id, constraint
}

// Package-level convenience wrappers — dispatch to defaultModule.

// Describe sets the default module's description. Panics if Init has not been called.
func Describe(s string) { mustDefault("Describe").Describe(s) }

// DependsOn declares a required dependency on the default module. Panics if
// Init has not been called. See Module.DependsOn for spec syntax.
func DependsOn(id string) { mustDefault("DependsOn").DependsOn(id) }

// Needs declares an optional dependency on the default module and returns the
// handler unchanged. Panics if Init has not been called. See Module.Needs for
// the full contract.
func Needs(id string, h http.HandlerFunc) http.HandlerFunc {
	return mustDefault("Needs").Needs(id, h)
}

// Resolve looks up a previously-registered client of type T by module ID.
// Returns the zero value of T and false when no client is registered for id
// (either because the dependency module isn't installed or no module has
// exported a T-typed client).
//
// Pairs with optional deps declared via ms.Needs: check ok before calling
// into T.
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
	_ = id
	var zero T
	return zero, false
}
