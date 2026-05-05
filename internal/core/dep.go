package core

// Dep is the configuration handle passed to ms.DependsOn / ms.Needs
// callbacks. It collects the set of relations the consumer wants to read
// from the dependency module's `mod_<id>` schema.
//
// Each Reads call records one relation name. Duplicates within the same
// callback collapse to one entry. Empty (no Reads calls) means the
// consumer doesn't read any relations from this dep — it integrates
// purely via events, internal HTTP, or ms.Resolve.
type Dep struct {
	reads []string
}

// Reads marks one relation name from the dependency's `mod_<id>` schema as
// a SELECT request. The catalog validates the name against the
// dependency's manifest at install time (it must match a name the
// dependency declared via ms.ExposeTable). After app-owner approval, the
// platform issues `GRANT SELECT` against the consumer's per-app DB role.
//
//	ms.DependsOn("@anna/oauth@^0.4.0", func(d *ms.Dep) {
//	    d.Reads("oauth_users")
//	    d.Reads("recent_orders")
//	})
func (d *Dep) Reads(name string) {
	d.reads = append(d.reads, name)
}

// configureDep applies any number of variadic configure callbacks to a
// fresh Dep and returns the accumulated reads list. Used by DependsOn /
// Needs.
func configureDep(configure []func(*Dep)) []string {
	if len(configure) == 0 {
		return nil
	}
	d := &Dep{}
	for _, fn := range configure {
		if fn != nil {
			fn(d)
		}
	}
	return d.reads
}
