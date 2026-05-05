package core

// ExposeTable declares a relation in the module's `mod_<id>` schema as
// part of the module's public READ API. Despite the name, it works for
// any Postgres relation kind — table, view, or materialized view — since
// `GRANT SELECT` applies uniformly.
//
// Read-only by design. There is no INSERT/UPDATE/DELETE grant on this
// surface. Cross-module *writes* go through events or internal HTTP
// endpoints, not raw GRANTs — that keeps each module's data ownership
// boundary auditable.
//
// Who reads what is decided on the consumer side, not here. Consumer
// modules name the relations they want via `ms.DependsOn(spec, func(d
// *ms.Dep) { d.Reads("...") })` (or the matching `ms.Needs` form for
// optional deps). When the app owner approves the install, the catalog
// emits one `GRANT SELECT` per requested relation. The contributor
// doesn't pre-list consumers — that allowlist model can't work in a
// marketplace where third parties show up after publish.
//
//	ms.ExposeTable("recent_orders")
//	ms.ExposeTable("audit_log")
//
// Calling `ExposeTable` multiple times with the same name is a no-op
// after the first call (first-wins, matches Permission/Schedule/Task).
func (m *Module) ExposeTable(name string) {
	m.registry.AddExposure(name)
}

// ExposeTable declares a public read surface on the default module.
// Panics if Init has not been called. See Module.ExposeTable for the
// contract.
func ExposeTable(name string) {
	mustDefault("ExposeTable").ExposeTable(name)
}
