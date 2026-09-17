package core

// Need is the configuration handle passed to ms.DependsOn /
// ms.OptionalDependOn callbacks. It collects what the consumer wants
// from a dependency module — relations to read, events to subscribe to,
// and whether the module still installs without it. The handle is
// opaque: Table, Event and Optional are the only mutators, so a caller
// can't bypass them by constructing a Need{...} literal.
type Need struct {
	tables   []string
	events   []string
	optional bool
}

// Table records a bare relation name from the dep's per-app tables as a
// SELECT request — the physical target is app_<id>."<prefix><table>" in
// the shared app tenant schema, not a cross-app mod_<id> schema. The
// catalog validates the name against the dep's exposed tables at install
// time; after app-owner approval, the platform issues GRANT SELECT
// against this consumer's per-app DB role.
//
//	ms.DependsOn("@anna/oauth@^0.4.0", func(n *ms.Need) {
//	    n.Table("oauth_users")
//	    n.Table("recent_orders")
//	})
func (n *Need) Table(name string) {
	n.tables = append(n.tables, name)
}

// Event records an event name the consumer subscribes to from this dep.
// Used by the catalog at install time to validate the event exists in
// the dep's manifest (Emits list) and to wire up routing.
//
//	ms.DependsOn("@bob/orders@^1", func(n *ms.Need) {
//	    n.Event("order_placed")
//	})
func (n *Need) Event(name string) {
	n.events = append(n.events, name)
}

// Optional marks a dependency declared with ms.DependsOn as OPTIONAL:
// the module installs and runs whether or not the dep is present, and
// the manifest carries "optional":true. It is the CALL-time counterpart
// of ms.OptionalDependOn, which scopes an optional dep to one event
// handler — use Optional when the code that needs the dep is an
// ordinary request path (an ms.CallDependencyPost, an ms.DependencyDB read)
// rather than an ms.OnEvent subscription.
//
//	ms.DependsOn("user-core@^1", func(n *ms.Need) {
//	    n.Optional()
//	})
//
// The consumer owns the absent case: a call or read against a dep the
// app has not installed fails, so the caller must degrade (e.g. show an
// id instead of a name) rather than fail its own request. If the same
// dep is also declared required anywhere, required wins — see
// Registry.AddDependency. Inside ms.OptionalDependOn it is a no-op.
func (n *Need) Optional() {
	n.optional = true
}
