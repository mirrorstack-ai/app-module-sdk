package core

import (
	"fmt"
	"strings"

	"github.com/Masterminds/semver/v3"

	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
)

// DependsOn declares a REQUIRED dependency on another module. Install
// fails if no published version of the dep matches the constraint.
//
// Spec syntax: "<id>" (any version) or "<id>@<constraint>" where
// constraint is an npm-style SemVer range. The id may be bare
// (`oauth-core`) or owner-prefixed (`@anna/oauth`). Examples:
//
//	ms.DependsOn("@anna/oauth")              // any version
//	ms.DependsOn("@anna/oauth@^1.2.0")       // >=1.2.0, <2.0.0
//	ms.DependsOn("@anna/oauth@1.2.3")        // exact
//	ms.DependsOn("oauth-core@>=1.2 <2")      // bare id, range
//
// Optional configure callbacks declare what the consumer wants from the
// dep — relation reads and event subscriptions:
//
//	ms.DependsOn("@anna/oauth@^0.4.0", func(n *ms.Need) {
//	    n.Table("oauth_users")
//	    n.Event("user_signed_in")
//	})
//
// The catalog validates each Table name against the dep's `mod_<id>`
// schema and each Event name against the dep's manifest at install
// time; after app-owner approval, the platform issues one GRANT SELECT
// per Table and wires event routing per Event.
//
// Panics on an invalid SemVer constraint — programmer error, not
// runtime input.
//
// For OPTIONAL deps (the consumer's handler only runs when the dep is
// installed — typically an event subscription), use ms.OptionalDependOn
// inside the handler-registration call.
func (m *Module) DependsOn(spec string, configure ...func(*Need)) {
	id, constraint := parseDepSpec(spec)
	tables, events := configureNeed(configure)
	m.registry.AddDependency(registry.Dependency{
		ID:       id,
		Version:  constraint,
		Optional: false,
		Tables:   tables,
		Events:   events,
	})
}

// OptionalDependOn declares an OPTIONAL dependency. Returns an
// OnEventOption that gets passed as the third+ argument of ms.OnEvent —
// the dep is "scoped" to that event handler. If the dep isn't installed,
// the event source doesn't exist, the event never fires, the handler
// never runs; the missing dep is harmless.
//
// Same spec syntax and Need-callback shape as DependsOn:
//
//	ms.OnEvent("@anna/billing/payment", onPayment,
//	    ms.OptionalDependOn("@anna/billing@^1", func(n *ms.Need) {
//	        n.Table("invoices")
//	    }))
//
// Repeated declarations of the same dep ID merge — Tables and Events
// accumulate across calls (set union); if the same ID is also declared
// required via DependsOn elsewhere, required wins for the optional
// flag, but Tables/Events still merge.
func OptionalDependOn(spec string, configure ...func(*Need)) OnEventOption {
	id, constraint := parseDepSpec(spec)
	tables, events := configureNeed(configure)
	dep := registry.Dependency{
		ID:       id,
		Version:  constraint,
		Optional: true,
		Tables:   tables,
		Events:   events,
	}
	return func(o *onEventOptions) {
		o.optionalDeps = append(o.optionalDeps, dep)
	}
}

// parseDepSpec splits "id@constraint" into its two parts. The id is
// required; the constraint is optional.
//
// The split uses the LAST `@` so `@<owner>/<name>@<version>` parses
// correctly — the owner-prefix `@` at position 0 stays inside the id.
// Bare `oauth-core@^1` (no owner prefix) still works because there's
// only one `@` to find. Panics on an invalid SemVer constraint.
func parseDepSpec(spec string) (id, constraint string) {
	at := strings.LastIndex(spec, "@")
	if at <= 0 {
		// No `@` (bare id) or `@` at position 0 (owner-prefixed without
		// version). Either way: no constraint segment.
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

// DependsOn declares a required dependency on the default module. Panics
// if Init has not been called. See Module.DependsOn for spec syntax and
// the Need-callback contract.
func DependsOn(spec string, configure ...func(*Need)) {
	mustDefault("DependsOn").DependsOn(spec, configure...)
}

// Resolve looks up a previously-registered client of type T by module ID.
// Returns the zero value of T and false when no client is registered for
// id (either because the dependency module isn't installed or no module
// has exported a T-typed client).
//
// v1 note: cross-module client wiring is not yet designed. This stub
// always returns (zero, false). Real resolution lands with the catalog's
// install machinery.
func Resolve[T any](id string) (T, bool) {
	_ = id
	var zero T
	return zero, false
}
