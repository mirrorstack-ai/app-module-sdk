package ids_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/internal/ids"
)

var uuidV4Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewUUID_Format(t *testing.T) {
	t.Parallel()
	u := ids.NewUUID()
	if !uuidV4Pattern.MatchString(u) {
		t.Errorf("NewUUID() = %q, want UUID v4 format", u)
	}
}

func TestNewUUID_Uniqueness(t *testing.T) {
	t.Parallel()
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		u := ids.NewUUID()
		if _, dup := seen[u]; dup {
			t.Fatalf("NewUUID() produced duplicate %q at iteration %d", u, i)
		}
		seen[u] = struct{}{}
	}
}

// TestFormatUUID_CanonicalText locks the text shape a co-located dev read
// depends on: pgx hands back a `uuid` column as [16]byte, and the consumer's
// join key is whatever string that becomes. A regression here does not fail
// loudly — it produces "[18 51 179 245 ...]" where a UUID was expected and the
// fetch-then-join silently matches nothing.
func TestFormatUUID_CanonicalText(t *testing.T) {
	t.Parallel()
	b := [16]byte{0x12, 0x33, 0xb3, 0xf5, 0x31, 0x52, 0x49, 0xc3, 0xb3, 0xbf, 0x6c, 0xd6, 0x5d, 0x87, 0x0a, 0x47}
	const want = "1233b3f5-3152-49c3-b3bf-6cd65d870a47"
	if got := ids.FormatUUID(b); got != want {
		t.Errorf("FormatUUID = %q, want %q", got, want)
	}
}

// TestFormatUUID_HyphenPositions pins the 8-4-4-4-12 grouping independently of
// the golden value above, so a byte-slicing typo that still round-trips one
// fixture cannot pass.
func TestFormatUUID_HyphenPositions(t *testing.T) {
	t.Parallel()
	got := ids.FormatUUID([16]byte{})
	if len(got) != 36 {
		t.Fatalf("FormatUUID length = %d, want 36", len(got))
	}
	for _, i := range []int{8, 13, 18, 23} {
		if got[i] != '-' {
			t.Errorf("FormatUUID()[%d] = %q, want '-' (8-4-4-4-12 grouping)", i, got[i])
		}
	}
}

func TestNewUUID_VersionAndVariantBits(t *testing.T) {
	t.Parallel()
	u := ids.NewUUID()
	// UUID format: xxxxxxxx-xxxx-Mxxx-Nxxx-xxxxxxxxxxxx
	// M = version (4), N first nibble must be 8, 9, a, or b (variant RFC 4122)
	parts := strings.Split(u, "-")
	if len(parts) != 5 {
		t.Fatalf("unexpected format: %q", u)
	}
	if parts[2][0] != '4' {
		t.Errorf("version nibble = %c, want 4", parts[2][0])
	}
	variant := parts[3][0]
	if variant != '8' && variant != '9' && variant != 'a' && variant != 'b' {
		t.Errorf("variant nibble = %c, want 8/9/a/b (RFC 4122)", variant)
	}
}
