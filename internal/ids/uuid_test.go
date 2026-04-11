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
