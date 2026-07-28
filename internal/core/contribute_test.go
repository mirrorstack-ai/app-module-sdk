package core

import (
	"encoding/json"
	"testing"
)

func TestContributesToParsesHostSpec(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		spec           string
		wantHost       string
		wantConstraint string
	}{
		{"bare", "oauth-core", "oauth-core", ""},
		{"owner prefixed", "@mirrorstack/oauth-core", "@mirrorstack/oauth-core", ""},
		{"owner prefixed pinned", "@mirrorstack/oauth-core@^0.1", "@mirrorstack/oauth-core", "^0.1"},
		{"bare pinned range", "oauth-core@>=0.1 <0.3", "oauth-core", ">=0.1 <0.3"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := New(Config{ID: "demo"})
			m.ContributesTo(tc.spec, "profile", map[string]string{"name": "demo"})
			got := m.registry.OutboundContributions()
			if len(got) != 1 {
				t.Fatalf("OutboundContributions() length = %d, want 1", len(got))
			}
			if got[0].Host != tc.wantHost || got[0].Constraint != tc.wantConstraint {
				t.Fatalf("host spec parsed as (%q, %q), want (%q, %q)",
					got[0].Host, got[0].Constraint, tc.wantHost, tc.wantConstraint)
			}
		})
	}
}

func TestContributesToRejectsInvalidDeclarations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		host string
		slot string
	}{
		{"invalid constraint", "oauth-core@not-a-version", "profile"},
		{"malformed id", "Oauth Core", "profile"},
		{"empty host", "", "profile"},
		{"empty slot", "oauth-core", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := New(Config{ID: "demo"})
			defer func() {
				if recover() == nil {
					t.Fatal("ContributesTo did not panic")
				}
			}()
			m.ContributesTo(tc.host, tc.slot, struct{}{})
		})
	}
}

func TestContributesToRedeclarationReplacesPayloadAndConstraint(t *testing.T) {
	t.Parallel()

	m, _ := New(Config{ID: "demo"})
	m.ContributesTo("oauth-core@^0.1", "profile", map[string]int{"revision": 1})
	m.ContributesTo("oauth-core@^0.2", "profile", map[string]int{"revision": 2})

	got := m.registry.OutboundContributions()
	if len(got) != 1 {
		t.Fatalf("OutboundContributions() length = %d, want 1", len(got))
	}
	if got[0].Constraint != "^0.2" || string(got[0].Payload) != `{"revision":2}` {
		t.Fatalf("redeclaration = %#v, want replacement payload and constraint", got[0])
	}
}

func TestOutboundContributionOmitsEmptyConstraint(t *testing.T) {
	t.Parallel()

	m, _ := New(Config{ID: "demo"})
	m.ContributesTo("oauth-core", "profile", struct{}{})
	raw, err := json.Marshal(m.registry.OutboundContributions()[0])
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["constraint"]; ok {
		t.Fatalf("unpinned contribution JSON contains constraint: %s", raw)
	}
}
