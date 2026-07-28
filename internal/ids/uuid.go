// Package ids provides identifier helpers shared across SDK packages.
// It is a leaf package with no internal imports so any SDK package can
// use it without creating a cycle.
package ids

import (
	"crypto/rand"
	"fmt"
)

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
