package core

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---- Describe / DependsOn / Needs / Resolve ----

func TestDescribe_PopulatesRegistry(t *testing.T) {
	t.Parallel()

	m, _ := New(Config{ID: "demo"})
	m.Describe("A demo module")
	if got := m.registry.Description(); got != "A demo module" {
		t.Errorf("Description = %q, want %q", got, "A demo module")
	}
}

func TestDependsOn_AlwaysRequired(t *testing.T) {
	t.Parallel()

	m, _ := New(Config{ID: "demo"})
	m.DependsOn("oauth-core")
	deps := m.registry.Dependencies()
	if len(deps) != 1 {
		t.Fatalf("len(Dependencies) = %d, want 1", len(deps))
	}
	if deps[0].ID != "oauth-core" || deps[0].Optional {
		t.Errorf("Dependencies[0] = %+v, want {ID:oauth-core, Optional:false}", deps[0])
	}
}

func TestNeeds_DeclaresOptionalDep(t *testing.T) {
	resetDefault(t)
	m := newTestModuleWithSecret(t, "demo")
	defaultModule = m

	handler := func(w http.ResponseWriter, r *http.Request) {}
	wrapped := Needs("video", handler)
	if wrapped == nil {
		t.Fatal("Needs returned nil handler")
	}

	deps := m.registry.Dependencies()
	if len(deps) != 1 {
		t.Fatalf("len(Dependencies) = %d, want 1", len(deps))
	}
	if deps[0].ID != "video" || !deps[0].Optional {
		t.Errorf("Dependencies[0] = %+v, want {ID:video, Optional:true}", deps[0])
	}
}

func TestNeeds_HandlerPassesThrough(t *testing.T) {
	resetDefault(t)
	m := newTestModuleWithSecret(t, "demo")
	defaultModule = m

	called := false
	handler := func(w http.ResponseWriter, r *http.Request) { called = true }
	wrapped := Needs("video", handler)

	rec := httptest.NewRecorder()
	wrapped(rec, httptest.NewRequest("GET", "/x", nil))
	if !called {
		t.Error("Needs wrapper did not call the underlying handler")
	}
}

func TestNeeds_RequiredWinsOverNeedsForSameID(t *testing.T) {
	resetDefault(t)
	m := newTestModuleWithSecret(t, "demo")
	defaultModule = m

	// Required first, then Needs with same id.
	DependsOn("video")
	_ = Needs("video", func(w http.ResponseWriter, r *http.Request) {})

	deps := m.registry.Dependencies()
	if len(deps) != 1 {
		t.Fatalf("len(Dependencies) = %d, want 1 (dedup)", len(deps))
	}
	if deps[0].Optional {
		t.Errorf("Dependencies[0].Optional = true, want false (required wins)")
	}
}

func TestNeeds_NestedDeclaresMultipleDeps(t *testing.T) {
	resetDefault(t)
	m := newTestModuleWithSecret(t, "demo")
	defaultModule = m

	_ = Needs("video", Needs("audit-log", func(w http.ResponseWriter, r *http.Request) {}))

	deps := m.registry.Dependencies()
	if len(deps) != 2 {
		t.Fatalf("len(Dependencies) = %d, want 2", len(deps))
	}
	ids := map[string]bool{deps[0].ID: deps[0].Optional, deps[1].ID: deps[1].Optional}
	for id, optional := range ids {
		if !optional {
			t.Errorf("dep %q: Optional = false, want true", id)
		}
	}
	if !ids["video"] || !ids["audit-log"] {
		t.Errorf("deps = %+v, want both video and audit-log", deps)
	}
}

func TestResolve_UnregisteredReturnsZeroAndFalse(t *testing.T) {
	t.Parallel()

	type fakeClient struct{ X int }
	got, ok := Resolve[fakeClient]("not-registered")
	if ok {
		t.Error("Resolve for unregistered id returned ok=true, want false")
	}
	if got.X != 0 {
		t.Errorf("Resolve returned non-zero value %+v, want zero", got)
	}
}

func TestDependsOn_VersionConstraintStoredInManifest(t *testing.T) {
	resetDefault(t)
	m := newTestModuleWithSecret(t, "demo")
	defaultModule = m

	DependsOn("oauth-core@^1.2.0")

	deps := m.registry.Dependencies()
	if len(deps) != 1 {
		t.Fatalf("len(deps) = %d, want 1", len(deps))
	}
	if deps[0].ID != "oauth-core" {
		t.Errorf("deps[0].ID = %q, want oauth-core", deps[0].ID)
	}
	if deps[0].Version != "^1.2.0" {
		t.Errorf("deps[0].Version = %q, want ^1.2.0", deps[0].Version)
	}
	if deps[0].Optional {
		t.Errorf("deps[0].Optional = true, want false")
	}
}

func TestNeeds_VersionConstraint(t *testing.T) {
	resetDefault(t)
	m := newTestModuleWithSecret(t, "demo")
	defaultModule = m

	_ = Needs("video@~1.2.0", func(w http.ResponseWriter, r *http.Request) {})

	deps := m.registry.Dependencies()
	if len(deps) != 1 {
		t.Fatalf("len(deps) = %d, want 1", len(deps))
	}
	if deps[0].ID != "video" || deps[0].Version != "~1.2.0" || !deps[0].Optional {
		t.Errorf("deps[0] = %+v, want {video, ~1.2.0, optional}", deps[0])
	}
}

func TestDependsOn_NoConstraintLeavesVersionEmpty(t *testing.T) {
	resetDefault(t)
	m := newTestModuleWithSecret(t, "demo")
	defaultModule = m

	DependsOn("oauth-core")

	deps := m.registry.Dependencies()
	if len(deps) != 1 || deps[0].Version != "" {
		t.Errorf("deps[0].Version = %q, want empty", deps[0].Version)
	}
}

func TestDependsOn_InvalidConstraintPanics(t *testing.T) {
	resetDefault(t)
	m := newTestModuleWithSecret(t, "demo")
	defaultModule = m

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on invalid SemVer constraint")
		}
	}()
	DependsOn("oauth-core@not-a-semver")
}

func TestParseDepSpec_SupportsCommonFormats(t *testing.T) {
	t.Parallel()

	cases := []struct {
		spec      string
		wantID    string
		wantVer   string
		wantPanic bool
	}{
		{"oauth-core", "oauth-core", "", false},
		{"oauth-core@^1.2.0", "oauth-core", "^1.2.0", false},
		{"oauth-core@~1.2.0", "oauth-core", "~1.2.0", false},
		{"oauth-core@1.2.3", "oauth-core", "1.2.3", false},
		{"oauth-core@>=1.2.0", "oauth-core", ">=1.2.0", false},
		{"oauth-core@1.x", "oauth-core", "1.x", false},
		{"oauth-core@", "oauth-core", "", false}, // empty constraint = any
	}
	for _, tc := range cases {
		t.Run(tc.spec, func(t *testing.T) {
			defer func() {
				r := recover()
				if tc.wantPanic && r == nil {
					t.Errorf("expected panic for spec=%q", tc.spec)
				} else if !tc.wantPanic && r != nil {
					t.Errorf("unexpected panic for spec=%q: %v", tc.spec, r)
				}
			}()
			gotID, gotVer := parseDepSpec(tc.spec)
			if gotID != tc.wantID || gotVer != tc.wantVer {
				t.Errorf("parseDepSpec(%q) = (%q, %q), want (%q, %q)", tc.spec, gotID, gotVer, tc.wantID, tc.wantVer)
			}
		})
	}
}

// ---- Dep callback (Reads) ----

func TestDependsOn_CallbackReadsRecorded(t *testing.T) {
	t.Parallel()

	m, _ := New(Config{ID: "demo"})
	m.DependsOn("@anna/oauth@^0.4.0", func(d *Dep) {
		d.Reads("oauth_users")
		d.Reads("recent_orders")
	})
	deps := m.registry.Dependencies()
	if len(deps) != 1 {
		t.Fatalf("len(Dependencies) = %d, want 1", len(deps))
	}
	want := []string{"oauth_users", "recent_orders"}
	if !slicesEqual(deps[0].Reads, want) {
		t.Errorf("Reads = %+v, want %+v", deps[0].Reads, want)
	}
	if deps[0].Version != "^0.4.0" {
		t.Errorf("Version = %q, want %q", deps[0].Version, "^0.4.0")
	}
}

func TestDependsOn_NoCallbackLeavesReadsEmpty(t *testing.T) {
	t.Parallel()

	m, _ := New(Config{ID: "demo"})
	m.DependsOn("@anna/oauth@^0.4.0")
	deps := m.registry.Dependencies()
	if len(deps[0].Reads) != 0 {
		t.Errorf("Reads = %+v, want empty (no callback supplied)", deps[0].Reads)
	}
}

func TestDependsOn_RepeatedCallbackMergesReads(t *testing.T) {
	t.Parallel()

	// Feature-flagged additions in different code paths can declare reads
	// across multiple calls; the registry merges them as a set union.
	m, _ := New(Config{ID: "demo"})
	m.DependsOn("@anna/oauth", func(d *Dep) { d.Reads("a"); d.Reads("b") })
	m.DependsOn("@anna/oauth", func(d *Dep) { d.Reads("b"); d.Reads("c") })
	deps := m.registry.Dependencies()
	if len(deps) != 1 {
		t.Fatalf("expected dedup to keep one entry, got %d", len(deps))
	}
	want := []string{"a", "b", "c"}
	if !slicesEqual(deps[0].Reads, want) {
		t.Errorf("Reads = %+v, want %+v", deps[0].Reads, want)
	}
}

func TestNeeds_CallbackReadsRecorded(t *testing.T) {
	resetDefault(t)
	m := newTestModuleWithSecret(t, "demo")
	defaultModule = m

	wrapped := Needs("@anna/billing@^1", func(http.ResponseWriter, *http.Request) {}, func(d *Dep) {
		d.Reads("invoices")
	})
	if wrapped == nil {
		t.Fatal("Needs returned nil handler")
	}

	deps := m.registry.Dependencies()
	if len(deps) != 1 || !deps[0].Optional {
		t.Fatalf("expected one optional dep, got %+v", deps)
	}
	if !slicesEqual(deps[0].Reads, []string{"invoices"}) {
		t.Errorf("Reads = %+v, want [invoices]", deps[0].Reads)
	}
}

func TestNeeds_RequiredWinsButReadsAccumulate(t *testing.T) {
	resetDefault(t)
	m := newTestModuleWithSecret(t, "demo")
	defaultModule = m

	// Optional first with one read, required second with another. The flag
	// upgrades to required (stricter), and Reads is the union.
	_ = Needs("@anna/oauth", func(http.ResponseWriter, *http.Request) {}, func(d *Dep) { d.Reads("a") })
	DependsOn("@anna/oauth", func(d *Dep) { d.Reads("b") })

	deps := m.registry.Dependencies()
	if len(deps) != 1 {
		t.Fatalf("expected one dep, got %+v", deps)
	}
	if deps[0].Optional {
		t.Error("expected Optional=false after required upgrade")
	}
	if !slicesEqual(deps[0].Reads, []string{"a", "b"}) {
		t.Errorf("Reads = %+v, want [a b]", deps[0].Reads)
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
