package registry

import (
	"fmt"
	"regexp"
	"slices"
)

// exposedTableNamePattern: Postgres-safe identifier. Lowercase, starts with a
// letter, only [a-z0-9_], up to 63 chars (the Postgres NAMEDATALEN ceiling).
// An exposed table lives under the module's `mod_<id>` schema; the platform
// composes the fully-qualified name itself when it issues GRANT SELECT.
var exposedTableNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// AddExposedTable records a table NAME as eligible for SELECT by a depending
// module. v1 is TABLES ONLY, read-only — the producer marks a relation
// readable; it does NOT name WHO reads it. The app owner (not the producer)
// decides which installed modules may read by approving a dependency. There is
// intentionally no per-consumer allowlist on the exposure itself.
//
// Name must match exposedTableNamePattern; validation panics, like the rest of
// the registry — an invalid declaration is a programmer error caught at module
// init, not a runtime input.
//
// Dedup: declaring the same name twice is a no-op (set union). ExposedTables
// returns the de-duplicated set sorted, so repeated/feature-flagged
// declarations compose safely and the manifest output is deterministic.
func (r *Registry) AddExposedTable(name string) {
	if !exposedTableNamePattern.MatchString(name) {
		panic(fmt.Sprintf(
			"mirrorstack/registry: ExposeTable(%q) name must be lowercase, start with a letter, only [a-z0-9_], <=63 chars",
			name,
		))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if slices.Contains(r.exposedTables, name) {
		return
	}
	r.exposedTables = append(r.exposedTables, name)
}

// ExposedTables returns a non-nil, SORTED, de-duplicated copy of all exposed
// table names. Sorting makes the manifest output deterministic (stable for
// prompt-cache and manifest-diffing) regardless of declaration order.
func (r *Registry) ExposedTables() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.exposedTables) == 0 {
		return []string{}
	}
	out := slices.Clone(r.exposedTables)
	slices.Sort(out)
	return out
}
