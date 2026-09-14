package dispatchpath

import (
	"net/url"
	"strings"
	"testing"
)

// The whole point of the prefix: the SAME composed address works whether the
// module's baked-in MS_DISPATCH_URL is the bare API host or that host plus the
// `/dispatch` mapping key. A module cannot be handed a new base without being
// re-provisioned, so the address has to survive both.
func TestJoin_BothBasesLandOnTheSameRoute(t *testing.T) {
	const surface = "/apps/a-456/usage"

	bare := Join("https://api.example.com", surface)
	keyed := Join("https://api.example.com/dispatch", surface)

	if bare != "https://api.example.com/v1/dispatch/apps/a-456/usage" {
		t.Errorf("bare host = %q", bare)
	}
	if keyed != "https://api.example.com/dispatch/v1/dispatch/apps/a-456/usage" {
		t.Errorf("keyed base = %q", keyed)
	}

	// The mapping key strips its own prefix before dispatch sees the path, so
	// route the two the way the edge does and they must be identical.
	if got, want := routed(t, bare), routed(t, keyed); got != want {
		t.Errorf("the two bases reach different routes: %q vs %q", got, want)
	} else if got != "/v1/dispatch"+surface {
		t.Errorf("routed path = %q, want %q", got, "/v1/dispatch"+surface)
	}
}

// routed is what dispatch's chi router receives: the URL path, with a leading
// `/dispatch` mapping key removed (the key strips itself).
func routed(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return strings.TrimPrefix(u.Path, "/dispatch")
}

// 🔴 The bare path is what a pre-fix module asks for, and on the bare host it
// has no route — that 404 is the bug. Assert the composed address is never it.
func TestJoin_NeverProducesTheBareIngressPath(t *testing.T) {
	for _, base := range []string{"https://api.example.com", "https://api.example.com/dispatch", "http://host.docker.internal:8083"} {
		got := Join(base, "/apps/a/usage")
		if u, err := url.Parse(got); err != nil {
			t.Fatalf("parse %q: %v", got, err)
		} else if u.Path == "/apps/a/usage" {
			t.Errorf("Join(%q) produced the unrouted bare path %q", base, u.Path)
		}
	}
}

func TestJoin_TrimsTrailingSlashOnBase(t *testing.T) {
	for _, base := range []string{"http://d:8083", "http://d:8083/", "http://d:8083//"} {
		if got, want := Join(base, "/apps/a/usage"), "http://d:8083/v1/dispatch/apps/a/usage"; got != want {
			t.Errorf("Join(%q) = %q, want %q", base, got, want)
		}
	}
}

func TestPrefix_IsTheKeySurvivingAddress(t *testing.T) {
	// A change here is a flag day: the platform must serve the new prefix
	// BEFORE any module asks for it. Pinned so that is a deliberate edit.
	if Prefix != "/v1/dispatch" {
		t.Errorf("Prefix = %q; changing it strands every already-deployed module", Prefix)
	}
}
