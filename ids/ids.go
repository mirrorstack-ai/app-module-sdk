// Package ids validates and normalizes identifiers carried across module
// boundaries.
package ids

import "strings"

const (
	maxModuleSlugBytes      = 16
	maxUIComponentNameBytes = 64
)

// ValidModuleSlug reports whether value is a catalog module slug. Slugs use a
// lowercase ASCII letter followed by at most 15 lowercase ASCII letters,
// digits, or hyphens. The shared 16-byte limit keeps generated database
// identifiers below PostgreSQL's NAMEDATALEN boundary.
func ValidModuleSlug(value string) bool {
	if len(value) == 0 || len(value) > maxModuleSlugBytes || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for index := 1; index < len(value); index++ {
		character := value[index]
		if (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

// ValidUUID reports whether value has the dashed 8-4-4-4-12 UUID text shape.
//
// This is deliberately a syntax validator rather than a UUID version policy:
// platform-owned identifiers may come from more than one UUID version, and a
// boundary that only needs a UUID-shaped join key must not reject one version
// while accepting another. Hex digits may use either case.
func ValidUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !isHex(character) {
			return false
		}
	}
	return true
}

// CanonicalModuleID converts either a dashed UUID or the SDK's identifier-safe
// m<32hex> form to a lowercase dashed UUID. Invalid input returns ("", false).
// Whitespace is never trimmed: accepting it at an identity boundary would make
// the value used for comparison differ from the value received on the wire.
func CanonicalModuleID(value string) (string, bool) {
	if value != strings.TrimSpace(value) {
		return "", false
	}
	if len(value) == 33 && value[0] == 'm' {
		raw := value[1:]
		for _, character := range raw {
			if !isHex(character) {
				return "", false
			}
		}
		value = raw[0:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:32]
	}
	if !ValidUUID(value) {
		return "", false
	}
	return strings.ToLower(value), true
}

// ValidUIComponentName reports whether value is a manifest UI component name.
// Names use an ASCII letter followed by at most 63 ASCII letters or digits.
// This is the stable name a mount request selects; the manifest separately
// maps it to a bundle export.
func ValidUIComponentName(value string) bool {
	if len(value) == 0 || len(value) > maxUIComponentNameBytes || !isASCIILetter(value[0]) {
		return false
	}
	for index := 1; index < len(value); index++ {
		if !isASCIILetter(value[index]) && (value[index] < '0' || value[index] > '9') {
			return false
		}
	}
	return true
}

func isASCIILetter(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
}

func isHex(character rune) bool {
	return character >= '0' && character <= '9' ||
		character >= 'a' && character <= 'f' ||
		character >= 'A' && character <= 'F'
}
