package core

import (
	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
)

// ExposeTable declares a relation in the module's `mod_<id>` schema as
// readable by the listed consumer modules. Despite the name, the call
// works for any Postgres relation kind — table, view, materialized view —
// since `GRANT SELECT` applies uniformly. Views are the recommended path
// (the contributor controls projection and rename safety via
// `CREATE OR REPLACE VIEW`); tables are exposed when consumers genuinely
// need raw access. The platform/catalog gates write GRANTs separately.
//
// Pair with the consumer module calling `ms.DependsOn("@<owner>/<this>.<name>")`
// — both sides must match for the read to actually happen. This is the
// "contribute" half of the rely/contribute pattern documented in
// docs/oauth-modules-plan.md.
//
//	ms.ExposeTable("recent_orders",
//	    "@anna/dashboard",
//	    "@anna/reporting",
//	)
//
// Each `readableBy` entry is an exact `@<owner>/<module>` reference. No
// wildcards — every consumer is listed individually so the GRANT surface
// is auditable from the source. Calling `ExposeTable` multiple times with
// the same name merges the reader lists (set union), so feature-flagged
// additions compose safely.
func (m *Module) ExposeTable(name string, readableBy ...string) {
	m.registry.AddExposure(registry.Exposure{
		Name:       name,
		ReadableBy: readableBy,
	})
}

// ExposeTable declares a readable relation on the default module. Panics
// if Init has not been called. See Module.ExposeTable for the contract.
func ExposeTable(name string, readableBy ...string) {
	mustDefault("ExposeTable").ExposeTable(name, readableBy...)
}
