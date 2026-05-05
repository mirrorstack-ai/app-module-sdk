package registry

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// ExposureKind distinguishes "I'm exposing a view" from "I'm exposing a raw
// table". Views are the recommended path — they let the data owner shape the
// public projection of their schema independently of the physical storage —
// but tables are supported for cases where the consumer needs raw write
// access (rare; rely on the catalog to gate write grants).
type ExposureKind string

const (
	ExposureKindView  ExposureKind = "view"
	ExposureKindTable ExposureKind = "table"
)

// Exposure declares one schema element (a view or table) and the set of
// modules permitted to read it. Consumed by the platform catalog at install
// time to translate into Postgres GRANTs against the consumers' DB roles.
//
// `Name` is a Postgres-identifier-shaped string — lowercase letters, digits,
// underscores, leading letter, ≤63 chars. The exposed object lives under
// the module's `mod_<id>` schema; the platform composes the fully-qualified
// name itself.
//
// `ReadableBy` entries are `@<owner>/<module>` patterns. Wildcards are
// supported: `@*/analytics` (any owner's analytics module), `@me/oauth-*`
// (my modules whose name starts with `oauth-`), `@*/*` (everyone — use
// sparingly). Empty list means "no consumers declared yet" — the platform
// emits no GRANT until the contributor names a reader. The detailed pattern
// matching lives on the catalog side; the SDK just enforces the shape.
type Exposure struct {
	Name       string       `json:"name"`
	Kind       ExposureKind `json:"kind"`
	ReadableBy []string     `json:"readableBy"`
}

// exposureNamePattern: Postgres-safe identifier. Lowercase, starts with a
// letter, only [a-z0-9_], up to 63 chars (the Postgres NAMEDATALEN ceiling).
var exposureNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// readerPattern: `@<owner-pattern>/<module-pattern>`. Each half is one
// non-empty segment of `[a-z0-9_*-]` characters — narrow enough to fail
// loud on typos like `me/foo` (missing `@`) or `@me//foo` (empty middle
// segment) without baking in the catalog's full glob semantics.
var readerPattern = regexp.MustCompile(`^@[a-z0-9_*\-]+/[a-z0-9_*\-]+$`)

// AddExposure records an exposure. Name must match exposureNamePattern;
// each ReadableBy entry must match readerPattern. Validation panics — like
// the rest of the registry, an invalid declaration is a programmer error
// caught at module init, not a runtime input.
//
// Dedup: if the same Name is declared twice, the second call replaces the
// first (last-wins). Different from how Routes / Emits dedup as first-wins
// — exposures are deliberately re-declarable so a module can extend its
// `ReadableBy` list across files (e.g., feature-gated additions).
func (r *Registry) AddExposure(e Exposure) {
	ValidateName("Expose", e.Name)
	if !exposureNamePattern.MatchString(e.Name) {
		panic(fmt.Sprintf(
			"mirrorstack/registry: Expose(%q) name must be lowercase, start with a letter, only [a-z0-9_], <=63 chars",
			e.Name,
		))
	}
	if e.Kind != ExposureKindView && e.Kind != ExposureKindTable {
		panic(fmt.Sprintf("mirrorstack/registry: Expose(%q) unknown kind %q", e.Name, e.Kind))
	}
	for _, reader := range e.ReadableBy {
		if !readerPattern.MatchString(reader) {
			panic(fmt.Sprintf(
				"mirrorstack/registry: Expose(%q) readableBy entry %q must match `@<owner>/<module>` (wildcards allowed: `@*/*`, `@me/oauth-*`, etc.)",
				e.Name, reader,
			))
		}
	}
	// Defensive copy so a caller mutating their slice after registration
	// can't change what we hold.
	e.ReadableBy = slices.Clone(e.ReadableBy)
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, existing := range r.exposures {
		if existing.Name == e.Name {
			r.exposures[i] = e
			return
		}
	}
	r.exposures = append(r.exposures, e)
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
			Kind:       e.Kind,
			ReadableBy: slices.Clone(e.ReadableBy),
		}
	}
	return out
}

// String returns a debug-friendly summary. Used in panic messages elsewhere.
func (e Exposure) String() string {
	return fmt.Sprintf("Exposure{%s %s readableBy=[%s]}", e.Kind, e.Name, strings.Join(e.ReadableBy, ","))
}
