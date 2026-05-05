package core

// Need is the configuration handle passed to ms.DependsOn /
// ms.OptionalDependOn callbacks. It collects what the consumer wants
// from a dependency module — relations to read and events to subscribe
// to. The handle is opaque: Table and Event are the only mutators, so a
// caller can't bypass them by constructing a Need{...} literal.
type Need struct {
	tables []string
	events []string
}

// Table records a relation name from the dep's `mod_<id>` schema as a
// SELECT request. The catalog validates the name against the dep's
// schema at install time (introspecting `pg_class`); after app-owner
// approval, the platform issues GRANT SELECT against this consumer's
// per-app DB role.
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

// configureNeed runs each variadic configure callback against a fresh
// Need and returns the accumulated tables and events lists. Used by
// DependsOn / OptionalDependOn.
func configureNeed(configure []func(*Need)) (tables, events []string) {
	if len(configure) == 0 {
		return nil, nil
	}
	n := &Need{}
	for _, fn := range configure {
		if fn != nil {
			fn(n)
		}
	}
	return n.tables, n.events
}
