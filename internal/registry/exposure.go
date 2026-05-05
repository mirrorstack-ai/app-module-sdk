package registry

import (
	"fmt"
	"regexp"
	"slices"
)

// Exposure declares one relation (table, view, or materialized view) in
// the module's `mod_<id>` schema as readable by a set of consumer modules.
// Consumed by the platform catalog at install time to translate into
// Postgres GRANTs against the consumers' DB roles. The kind is not
// recorded — `GRANT SELECT` applies uniformly to all relation kinds, so
// the SDK doesn't need to distinguish them. The catalog can introspect
// `pg_class` at publish time if a UI needs to render the kind.
//
// `Name` is a Postgres-identifier-shaped string — lowercase letters, digits,
// underscores, leading letter, ≤63 chars. The exposed object lives under
// the module's `mod_<id>` schema; the platform composes the fully-qualified
// name itself.
//
// `ReadableBy` entries are exact `@<owner>/<module>` references — no
// wildcards. Matching by module *name* across owners (`@*/analytics`)
// would only be meaningful if the platform had a module-spec system
// declaring what a module named `analytics` must implement; it doesn't,
// so each consumer is listed explicitly and the GRANT surface stays
// auditable from the source. Empty list means "no consumers declared
// yet" — the platform emits no GRANT until the contributor names a
// reader.
type Exposure struct {
	Name       string   `json:"name"`
	ReadableBy []string `json:"readableBy"`
}

// exposureNamePattern: Postgres-safe identifier. Lowercase, starts with a
// letter, only [a-z0-9_], up to 63 chars (the Postgres NAMEDATALEN ceiling).
var exposureNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// readerPattern: exact `@<owner>/<module>`. Each half is one non-empty
// segment of `[a-z0-9_-]` characters. Both halves must be concrete
// identifiers — no `*`, no wildcards. The platform translates each entry
// into a Postgres GRANT against that specific consumer module's DB role.
var readerPattern = regexp.MustCompile(`^@[a-z0-9_\-]+/[a-z0-9_\-]+$`)

// AddExposure records an exposure. Name must match exposureNamePattern;
// each ReadableBy entry must match readerPattern. Validation panics — like
// the rest of the registry, an invalid declaration is a programmer error
// caught at module init, not a runtime input.
//
// Dedup: if the same Name is declared twice, ReadableBy entries from both
// calls are merged (set union). A security-adjacent declaration like
// `ms.ExposeTable("orders", "@anna/analytics")` losing the
// `@anna/analytics` reader because a second file forgets to re-list it
// would be surprising. Merging makes feature-flagged additions compose
// safely (each call adds; nothing silently drops).
func (r *Registry) AddExposure(e Exposure) {
	ValidateName("Expose", e.Name)
	if !exposureNamePattern.MatchString(e.Name) {
		panic(fmt.Sprintf(
			"mirrorstack/registry: Expose(%q) name must be lowercase, start with a letter, only [a-z0-9_], <=63 chars",
			e.Name,
		))
	}
	for _, reader := range e.ReadableBy {
		if !readerPattern.MatchString(reader) {
			panic(fmt.Sprintf(
				"mirrorstack/registry: Expose(%q) readableBy entry %q must match exact `@<owner>/<module>` — wildcards are not supported",
				e.Name, reader,
			))
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, existing := range r.exposures {
		if existing.Name != e.Name {
			continue
		}
		r.exposures[i].ReadableBy = mergeReaders(existing.ReadableBy, e.ReadableBy)
		return
	}
	// Defensive copy so a caller mutating their slice after registration
	// can't change what we hold.
	e.ReadableBy = slices.Clone(e.ReadableBy)
	r.exposures = append(r.exposures, e)
}

// mergeReaders returns the set union of two reader slices, preserving the
// order: existing entries stay where they are, new entries append in
// declaration order. Order matters only for the manifest's deterministic
// output; the catalog's GRANT emission is order-independent.
func mergeReaders(existing, additions []string) []string {
	if len(additions) == 0 {
		return existing
	}
	seen := make(map[string]struct{}, len(existing)+len(additions))
	for _, r := range existing {
		seen[r] = struct{}{}
	}
	out := slices.Clone(existing)
	for _, r := range additions {
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	return out
}

// Exposures returns a non-nil clone in registration order.
func (r *Registry) Exposures() []Exposure {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.exposures == nil {
		return []Exposure{}
	}
	out := make([]Exposure, len(r.exposures))
	for i, e := range r.exposures {
		out[i] = Exposure{
			Name:       e.Name,
			ReadableBy: slices.Clone(e.ReadableBy),
		}
	}
	return out
}
