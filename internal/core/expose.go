package core

// ExposeTable marks one of this module's per-app tables as eligible to be
// read (SELECT) by a depending module installed in the same app. Declare the
// bare table name; the platform resolves it to the physical form —
// app_<id>."<prefix><table>" in each app's tenant schema — when granting.
// It is a pure DECLARATION — no runtime, no return value — recorded in the
// manifest under `exposes.tables` so the platform catalog can issue GRANT
// SELECT after the app owner approves a dependency. Cross-app module state
// (the mod_<id> schema) is NOT shareable this way.
//
// v1 is TABLES ONLY, read-only. The producer marks a table READABLE; it does
// NOT name WHO reads it. The app owner — the trust root — decides which
// installed modules may read by approving their declared dependency. There is
// intentionally no per-consumer allowlist here: a marketplace's consumers are
// third parties, so a publisher-controlled reader list is the wrong trust
// model.
//
// Panics on an empty or otherwise invalid table identifier (lowercase, leading
// letter, [a-z0-9_], <=63 chars). Call from startup code, not a request
// handler.
//
//	ms.ExposeTable("orders")
func (m *Module) ExposeTable(name string) {
	m.registry.AddExposedTable(name)
}

// ExposeTable declares an exposed table on the default Module created by
// Init(). Panics before Init — matches Platform/Public/Internal/Emits.
func ExposeTable(name string) {
	mustDefault("ExposeTable").ExposeTable(name)
}
