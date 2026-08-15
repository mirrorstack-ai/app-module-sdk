// Package ids provides identifier helpers shared across SDK packages.
// It is a leaf package with no internal imports so any SDK package can
// use it without creating a cycle.
package ids

import (
	"crypto/rand"
	"fmt"
	"regexp"
	"strings"
)

var canonicalUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var compactModuleIDPattern = regexp.MustCompile(`^m[0-9a-f]{32}$`)

// NewUUID returns a random UUID v4 string (version 4, variant RFC 4122).
// Panics if crypto/rand.Read fails — callers cannot recover from entropy
// exhaustion on this path.
func NewUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("mirrorstack/ids: crypto/rand.Read failed: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant RFC 4122
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// FormatUUID renders the 16-byte binary form Postgres returns for a `uuid`
// column as the canonical 8-4-4-4-12 hyphenated text form. pgx decodes an
// unregistered `uuid` into [16]byte, so a raw row map hands the consumer
// something whose fmt.Sprint is "[202 60 140 239 ...]" — a silently corrupted
// join key. Same %x idiom NewUUID already uses, so the two can never disagree
// on the text shape.
func FormatUUID(b [16]byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// NormalizeModuleID converts the platform's canonical UUID identity to the
// identifier-safe m<32hex> form used by module schemas and manifests. It also
// accepts that compact form unchanged. Other legacy/dev IDs are deliberately
// outside this helper even though Config still accepts them for compatibility.
func NormalizeModuleID(value string) (string, bool) {
	if compactModuleIDPattern.MatchString(value) {
		return value, true
	}
	if !canonicalUUIDPattern.MatchString(value) {
		return "", false
	}
	return "m" + strings.ReplaceAll(value, "-", ""), true
}

// CanonicalModuleID converts an identifier-safe module ID back to the
// canonical UUID carried in broker claims. Canonical UUID input is returned
// unchanged so callers can compare identities without depending on transport
// representation.
func CanonicalModuleID(value string) (string, bool) {
	if canonicalUUIDPattern.MatchString(value) {
		return value, true
	}
	if !compactModuleIDPattern.MatchString(value) {
		return "", false
	}
	hex := value[1:]
	return hex[:8] + "-" + hex[8:12] + "-" + hex[12:16] + "-" + hex[16:20] + "-" + hex[20:], true
}
