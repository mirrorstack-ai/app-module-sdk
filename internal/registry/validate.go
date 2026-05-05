package registry

import (
	"regexp"
	"strings"
)

// depIDPattern accepts either a bare module ID (`oauth-core`,
// `billing_engine`) or an owner-prefixed reference (`@anna/oauth`,
// `@anna/oauth-google`). The owner-prefixed form is what catalog-published
// modules look like once the catalog assigns ownership; the bare form is
// kept for pre-catalog and platform-internal deps.
//
// Each segment is `[a-z][a-z0-9_-]*` so it composes safely with Postgres
// identifier rules and the storage-prefix grammar
// (<username>_<slug>_<table>). The Config.Slug regex is stricter (kebab
// only); this one is more permissive to accept both prefix shapes
// uniformly.
var depIDPattern = regexp.MustCompile(`^(@[a-z][a-z0-9_-]*/)?[a-z][a-z0-9_-]*$`)

// ValidateDepID rejects dependency IDs that don't match the bare or
// owner-prefixed shape. Used by AddDependency in place of ValidateName,
// which is too restrictive (it forbids `/` and `@`).
func ValidateDepID(spec string) {
	if !depIDPattern.MatchString(spec) {
		panic("mirrorstack/registry: DependsOn(" + spec + ") must match `<id>` or `@<owner>/<id>` (lowercase, leading letter, [a-z0-9_-])")
	}
}

// ValidateName rejects names that are empty, contain a path
// separator (/, \), contain a dot-segment (..), contain whitespace, or
// contain a null byte. The name is concatenated into a URL path by the
// SDK's event/cron handlers, so any character that chi might normalize
// or that an HTTP client cannot transmit safely is forbidden.
//
// SECURITY: this is the registry-level invariant. Bypassing the
// user-facing Module.OnEvent / Cron / Emits API and calling AddSubscribe
// / AddSchedule / AddEmit directly still triggers this guard, so the
// SDK's manifest cannot contain a name that would mismatch the chi route
// table or would serialize to malformed JSON for downstream consumers.
//
// Null byte (\x00) is blocked because it produces valid JSON but breaks
// many downstream consumers (shell, log parsers, PostgreSQL text columns).
// Unicode whitespace beyond ASCII is not blocked — chi's pattern matcher
// is byte-oriented and a Unicode-whitespace name would simply produce a
// dead handler, not a security issue.
//
// kind is the user-facing API name (e.g., "OnEvent", "Cron", "Emits", "Record")
// used in the panic message so callers see which call failed validation.
func ValidateName(kind, name string) {
	if name == "" {
		panic("mirrorstack/registry: " + kind + " name cannot be empty")
	}
	if strings.ContainsAny(name, "/\\ \t\n\r\x00") {
		panic("mirrorstack/registry: " + kind + "(" + name + ") contains a path separator, whitespace, or null byte")
	}
	if strings.Contains(name, "..") {
		panic("mirrorstack/registry: " + kind + "(" + name + ") contains '..'")
	}
}
