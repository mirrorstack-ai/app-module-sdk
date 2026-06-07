package core

import (
	"net/http"
	"slices"
	"testing"
)

// ---- Config.Description / DependsOn / OptionalDependOn / Resolve ----

func TestConfigDescription_PopulatesRegistry(t *testing.T) {
	t.Parallel()

	m, _ := New(Config{ID: "demo", Description: "A demo module"})
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

func TestDependsOn_VersionConstraintStored(t *testing.T) {
	t.Parallel()

	m, _ := New(Config{ID: "demo"})
	m.DependsOn("oauth-core@^1.2.0")
	deps := m.registry.Dependencies()
	if deps[0].ID != "oauth-core" || deps[0].Version != "^1.2.0" {
		t.Errorf("deps[0] = %+v, want {oauth-core, ^1.2.0}", deps[0])
	}
}

func TestDependsOn_OwnerPrefixedSpec(t *testing.T) {
	t.Parallel()

	// `@<owner>/<name>@<version>` must split at the LAST @ — the
	// owner-prefix @ at position 0 stays inside the id.
	m, _ := New(Config{ID: "demo"})
	m.DependsOn("@anna/oauth@^0.4.0")
	deps := m.registry.Dependencies()
	if deps[0].ID != "@anna/oauth" || deps[0].Version != "^0.4.0" {
		t.Errorf("deps[0] = %+v, want {@anna/oauth, ^0.4.0}", deps[0])
	}
}

func TestDependsOn_NoConstraintLeavesVersionEmpty(t *testing.T) {
	t.Parallel()

	m, _ := New(Config{ID: "demo"})
	m.DependsOn("oauth-core")
	deps := m.registry.Dependencies()
	if deps[0].Version != "" {
		t.Errorf("deps[0].Version = %q, want empty", deps[0].Version)
	}
}

func TestDependsOn_InvalidConstraintPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on invalid SemVer constraint")
		}
	}()
	m, _ := New(Config{ID: "demo"})
	m.DependsOn("oauth-core@not-a-semver")
}

func TestDependsOn_NeedCallback_TablesAndEvents(t *testing.T) {
	t.Parallel()

	m, _ := New(Config{ID: "demo"})
	m.DependsOn("@bob/orders@^0.4.0", func(n *Need) {
		n.Table("oauth_users")
		n.Table("recent_orders")
		n.Event("order_placed")
	})
	deps := m.registry.Dependencies()
	if len(deps) != 1 {
		t.Fatalf("len(deps) = %d, want 1", len(deps))
	}
	if !slices.Equal(deps[0].Tables, []string{"oauth_users", "recent_orders"}) {
		t.Errorf("Tables = %+v, want [oauth_users recent_orders]", deps[0].Tables)
	}
	if !slices.Equal(deps[0].Events, []string{"order_placed"}) {
		t.Errorf("Events = %+v, want [order_placed]", deps[0].Events)
	}
}

func TestDependsOn_NoCallbackLeavesTablesAndEventsEmpty(t *testing.T) {
	t.Parallel()

	m, _ := New(Config{ID: "demo"})
	m.DependsOn("@anna/oauth@^0.4.0")
	deps := m.registry.Dependencies()
	if len(deps[0].Tables) != 0 || len(deps[0].Events) != 0 {
		t.Errorf("expected empty Tables/Events, got Tables=%+v Events=%+v", deps[0].Tables, deps[0].Events)
	}
}

func TestDependsOn_RepeatedMergesTablesAndEvents(t *testing.T) {
	t.Parallel()

	// Feature-flagged additions in different code paths can declare
	// reads/events across multiple calls; the registry merges as a set
	// union so nothing is silently dropped.
	m, _ := New(Config{ID: "demo"})
	m.DependsOn("@anna/oauth", func(n *Need) {
		n.Table("a")
		n.Event("x")
	})
	m.DependsOn("@anna/oauth", func(n *Need) {
		n.Table("a")
		n.Table("b")
		n.Event("y")
	})
	deps := m.registry.Dependencies()
	if len(deps) != 1 {
		t.Fatalf("expected one entry after merge, got %d", len(deps))
	}
	if !slices.Equal(deps[0].Tables, []string{"a", "b"}) {
		t.Errorf("Tables = %+v, want [a b]", deps[0].Tables)
	}
	if !slices.Equal(deps[0].Events, []string{"x", "y"}) {
		t.Errorf("Events = %+v, want [x y]", deps[0].Events)
	}
}

func TestOptionalDependOn_RegistersOptionalDepThroughOnEvent(t *testing.T) {
	resetDefault(t)
	m := newTestModuleWithSecret(t, "demo")
	defaultModule = m

	OnEvent("payment", func(w http.ResponseWriter, r *http.Request) {},
		OptionalDependOn("@anna/billing@^1", func(n *Need) {
			n.Table("invoices")
		}))

	deps := m.registry.Dependencies()
	if len(deps) != 1 {
		t.Fatalf("len(deps) = %d, want 1", len(deps))
	}
	if !deps[0].Optional {
		t.Error("expected Optional=true for OptionalDependOn")
	}
	if deps[0].ID != "@anna/billing" || deps[0].Version != "^1" {
		t.Errorf("deps[0] = %+v, want {@anna/billing, ^1, optional}", deps[0])
	}
	if !slices.Equal(deps[0].Tables, []string{"invoices"}) {
		t.Errorf("Tables = %+v, want [invoices]", deps[0].Tables)
	}
}

func TestOptionalDependOn_RequiredWinsButTablesMerge(t *testing.T) {
	resetDefault(t)
	m := newTestModuleWithSecret(t, "demo")
	defaultModule = m

	// Optional-via-OnEvent first with one table, required-top-level
	// second with another. The flag upgrades to required, Tables is the
	// union.
	OnEvent("e", func(w http.ResponseWriter, r *http.Request) {},
		OptionalDependOn("@anna/oauth", func(n *Need) { n.Table("a") }))
	DependsOn("@anna/oauth", func(n *Need) { n.Table("b") })

	deps := m.registry.Dependencies()
	if len(deps) != 1 {
		t.Fatalf("expected one dep, got %+v", deps)
	}
	if deps[0].Optional {
		t.Error("expected Optional=false after required upgrade")
	}
	if !slices.Equal(deps[0].Tables, []string{"a", "b"}) {
		t.Errorf("Tables = %+v, want [a b]", deps[0].Tables)
	}
}

func TestOnEvent_HandlerStillRegistersWithoutOptions(t *testing.T) {
	// OnEvent with no opts is the prior behavior — handler mounts on
	// Internal scope, subscription recorded.
	resetDefault(t)
	m := newTestModuleWithSecret(t, "demo")
	defaultModule = m

	called := false
	OnEvent("video.completed", func(w http.ResponseWriter, r *http.Request) { called = true })

	// Hit the registered route via the auth-bypassing internal-secret path.
	rec := doRequestWithSecret(t, m.Router(), "POST", "/__mirrorstack/events/video.completed", "secret")
	if rec.Code != 200 || !called {
		t.Errorf("OnEvent without opts didn't dispatch: code=%d called=%v", rec.Code, called)
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
		{"oauth-core@", "oauth-core", "", false},
		{"@anna/oauth", "@anna/oauth", "", false},
		{"@anna/oauth@^0.4.0", "@anna/oauth", "^0.4.0", false},
		{"@anna/oauth-google@1.0.0", "@anna/oauth-google", "1.0.0", false},
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
