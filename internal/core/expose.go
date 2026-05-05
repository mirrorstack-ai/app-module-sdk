package core

import (
	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
)

// ExposeView declares that a view in the module's `mod_<id>` schema is
// readable by the listed consumer modules. Used by the catalog at install
// time to translate into Postgres GRANTs against the consumers' DB roles.
//
// Pair with the consumer module calling `ms.DependsOn("@<owner>/<this>.<view>")`
// — both sides must match for the read to actually happen. This is the
// "contribute" half of the rely/contribute pattern documented in
// docs/oauth-modules-plan.md.
//
//	ms.ExposeView("recent_orders",
//	    "@anna/dashboard",
//	    "@anna/reporting",
//	)
//
// Each `readableBy` entry is an exact `@<owner>/<module>` reference. No
// wildcards — every consumer is listed individually so the GRANT surface
// is auditable from the source. Calling `ExposeView` multiple times with
// the same name merges the reader lists (set union), so feature-flagged
// additions compose safely.
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
//	ms.ExposeTable("audit_log_writes", "@security/audit-collector")
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
