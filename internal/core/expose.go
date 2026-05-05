package core

import (
	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
)

// ExposeView declares that a view in the module's `mod_<id>` schema is
// readable by the listed module patterns. Used by the catalog at install
// time to translate into Postgres GRANTs against the consumers' DB roles.
//
// Pair with the consumer module calling `ms.DependsOn("@<owner>/<this>.<view>")`
// — both sides must match for the read to actually happen. This is the
// "contribute" half of the rely/contribute pattern documented in
// docs/oauth-modules-plan.md.
//
//	ms.ExposeView("recent_orders",
//	    "@*/analytics",
//	)
//
// Wildcards are supported: `@me/oauth-*` for "any of my own oauth modules",
// `@*/analytics` for "any owner's analytics module", `@*/*` for everyone
// (use sparingly — wider audit surface). Detailed pattern semantics live
// on the catalog side; the SDK enforces only the `@<owner>/<module>` shape.
//
// Last-call-wins on the same view name, so feature-flagged additions to the
// allow list compose by re-declaring with the merged set.
func (m *Module) ExposeView(name string, readableBy ...string) {
	m.registry.AddExposure(registry.Exposure{
		Name:       name,
		Kind:       registry.ExposureKindView,
		ReadableBy: readableBy,
	})
}

// ExposeTable is the rarely-used twin of ExposeView. Prefer views: they let
// you reshape the public projection of your data independently of how it's
// stored. Tables are exposed when consumers genuinely need raw access (and
// the catalog still gates write GRANTs separately).
//
//	ms.ExposeTable("audit_log_writes", "@*/audit-log")
func (m *Module) ExposeTable(name string, readableBy ...string) {
	m.registry.AddExposure(registry.Exposure{
		Name:       name,
		Kind:       registry.ExposureKindTable,
		ReadableBy: readableBy,
	})
}

// Package-level convenience wrappers — dispatch to defaultModule.

// ExposeView declares a readable view on the default module. Panics if Init
// has not been called. See Module.ExposeView for the contract.
func ExposeView(name string, readableBy ...string) {
	mustDefault("ExposeView").ExposeView(name, readableBy...)
}

// ExposeTable declares a readable table on the default module. Panics if
// Init has not been called. See Module.ExposeTable for the contract.
func ExposeTable(name string, readableBy ...string) {
	mustDefault("ExposeTable").ExposeTable(name, readableBy...)
}
