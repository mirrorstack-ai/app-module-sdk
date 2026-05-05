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
//	ms.DependsOn("@anna/oauth")              // any version
//	ms.DependsOn("@anna/oauth@^1.2.0")       // >=1.2.0, <2.0.0
//	ms.DependsOn("@anna/oauth@~1.2.0")       // >=1.2.0, <1.3.0
//	ms.DependsOn("@anna/oauth@>=1.2 <2")     // explicit range
//	ms.DependsOn("@anna/oauth@1.2.3")        // exact
//
// Optional configure callbacks declare cross-module READ requests. Each
// `d.Reads(name)` names a relation from the dependency's public READ
// surface (declared via that module's ms.ExposeTable). At install time
// the catalog validates the names exist on the dependency's manifest;
// after app-owner approval, one `GRANT SELECT` is emitted per entry
// against this module's per-app DB role.
//
//	ms.DependsOn("@anna/oauth@^0.4.0", func(d *ms.Dep) {
//	    d.Reads("oauth_users")
//	    d.Reads("recent_orders")
//	})
//
// Panics at call time on an invalid SemVer constraint — programmer error,
// not runtime input.
//
// For OPTIONAL deps (the module works standalone but integrates with the
// dependency when present), use ms.Needs at the handler registration site.
func (m *Module) DependsOn(spec string, configure ...func(*Dep)) {
	id, constraint := parseDepSpec(spec)
	m.registry.AddDependency(registry.Dependency{
		ID:       id,
		Version:  constraint,
		Optional: false,
		Reads:    configureDep(configure),
	})
}

// Needs declares an OPTIONAL dependency on another module and returns the
// handler unchanged. Designed to wrap handlers passed to ms.OnEvent, ms.Cron,
// chi routes — anywhere a handler is registered where the work may use the
// dependency.
//
// Spec syntax mirrors DependsOn: "id" or "id@constraint".
//
//	ms.OnEvent("video.completed", ms.Needs("@anna/video@^1", onCompleted))
//	ms.Cron("cleanup", "0 3 * * *", ms.Needs("@anna/storage", runCleanup))
//
// Optional configure callbacks declare cross-module READ requests, same
// shape as DependsOn — the catalog only emits the GRANTs when the dep
// actually gets installed:
//
//	ms.OnEvent("payment", ms.Needs("@anna/billing@^1", onPayment, func(d *ms.Dep) {
//	    d.Reads("invoices")
//	}))
//
// Nest Needs calls to declare multiple optional deps for one handler:
//
//	ms.OnEvent("payment",
//	    ms.Needs("@anna/billing@^1", ms.Needs("@anna/audit-log", onPayment)))
//
// Dedup: if the same ID has also been declared required via ms.DependsOn
// elsewhere, required wins for the optional flag, but Reads accumulate
// across calls (set union).
func (m *Module) Needs(spec string, h http.HandlerFunc, configure ...func(*Dep)) http.HandlerFunc {
	id, constraint := parseDepSpec(spec)
	m.registry.AddDependency(registry.Dependency{
		ID:       id,
		Version:  constraint,
		Optional: true,
		Reads:    configureDep(configure),
	})
	return h
}

// parseDepSpec splits "id@constraint" into its two parts. The id is required;
// the constraint is optional. Panics on an invalid SemVer constraint.
//
// The split uses the LAST `@` so `@<owner>/<name>@<version>` parses
// correctly — the owner-prefixed `@` at position 0 stays inside the id.
// Bare `oauth-core@^1` (no owner prefix) still works because there's only
// one `@` to find.
func parseDepSpec(spec string) (id, constraint string) {
	at := strings.LastIndex(spec, "@")
	// No `@` at all → no constraint. Position 0 means the whole spec is
	// `@owner/name` (an owner-prefixed id without a version), so also no
	// constraint.
	if at <= 0 {
		return spec, ""
	}
	id, constraint = spec[:at], spec[at+1:]
	if constraint == "" {
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
// Init has not been called. See Module.DependsOn for spec syntax and the
// optional Reads-callback contract.
func DependsOn(spec string, configure ...func(*Dep)) {
	mustDefault("DependsOn").DependsOn(spec, configure...)
}

// Needs declares an optional dependency on the default module and returns the
// handler unchanged. Panics if Init has not been called. See Module.Needs for
// the full contract.
func Needs(spec string, h http.HandlerFunc, configure ...func(*Dep)) http.HandlerFunc {
	return mustDefault("Needs").Needs(spec, h, configure...)
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
