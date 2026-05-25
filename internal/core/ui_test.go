package core

import (
	"slices"
	"testing"
)

// ---- RegisterUI: storage ----

func TestRegisterUI_PopulatesRegistry(t *testing.T) {
	t.Parallel()

	m, _ := New(Config{ID: "demo"})
	m.RegisterUI(ModuleUI{
		Components: []UIComponent{
			{Name: "SettingsForm", Export: "SettingsForm", Props: []UIProp{
				{Key: "appId", Type: "text", Required: true},
			}},
		},
		DefaultPages: []UIPage{
			{Route: "/", Title: "OAuth Settings", Export: "SettingsPage"},
		},
	})

	got := m.registry.UI()
	if got == nil {
		t.Fatal("UI() returned nil after RegisterUI")
	}
	if len(got.Components) != 1 || got.Components[0].Name != "SettingsForm" {
		t.Errorf("Components = %+v", got.Components)
	}
	if len(got.DefaultPages) != 1 || got.DefaultPages[0].Title != "OAuth Settings" {
		t.Errorf("DefaultPages = %+v", got.DefaultPages)
	}
}

func TestRegisterUI_NotCalled_UIReturnsNil(t *testing.T) {
	t.Parallel()

	m, _ := New(Config{ID: "demo"})
	if got := m.registry.UI(); got != nil {
		t.Errorf("UI() = %+v before RegisterUI, want nil", got)
	}
}

func TestRegisterUI_LastWriteWins(t *testing.T) {
	t.Parallel()

	m, _ := New(Config{ID: "demo"})
	m.RegisterUI(ModuleUI{
		DefaultPages: []UIPage{{Route: "/", Title: "First", Export: "First"}},
	})
	m.RegisterUI(ModuleUI{
		DefaultPages: []UIPage{{Route: "/", Title: "Second", Export: "Second"}},
	})

	got := m.registry.UI()
	if len(got.DefaultPages) != 1 || got.DefaultPages[0].Title != "Second" {
		t.Errorf("after second RegisterUI, DefaultPages = %+v, want only [{Second}]", got.DefaultPages)
	}
}

func TestRegisterUI_StoresDeepCopy(t *testing.T) {
	t.Parallel()

	m, _ := New(Config{ID: "demo"})
	pages := []UIPage{{Route: "/", Title: "Original", Export: "P"}}
	m.RegisterUI(ModuleUI{DefaultPages: pages})

	pages[0].Title = "Mutated"

	got := m.registry.UI()
	if got.DefaultPages[0].Title != "Original" {
		t.Errorf("registry mutated through input slice: Title = %q", got.DefaultPages[0].Title)
	}
}

func TestRegisterUI_UIReturnsDeepCopy(t *testing.T) {
	t.Parallel()

	m, _ := New(Config{ID: "demo"})
	m.RegisterUI(ModuleUI{
		DefaultPages: []UIPage{{Route: "/", Title: "Original", Export: "P"}},
	})

	first := m.registry.UI()
	first.DefaultPages[0].Title = "Mutated"

	second := m.registry.UI()
	if second.DefaultPages[0].Title != "Original" {
		t.Errorf("registry mutated through UI() return: Title = %q", second.DefaultPages[0].Title)
	}
}

// ---- RegisterUI: page route validation ----

func TestRegisterUI_RootRouteIsIndex(t *testing.T) {
	t.Parallel()

	// "/" is the index route — not subject to the segment regex.
	m, _ := New(Config{ID: "demo"})
	m.RegisterUI(ModuleUI{
		DefaultPages: []UIPage{{Route: "/", Title: "Index", Export: "P"}},
	})
}

func TestRegisterUI_ValidRouteVariations(t *testing.T) {
	t.Parallel()

	valid := []string{
		"/",
		"/a", "/1", "/settings", "/audit-log", "/v2-api", "/abc123", "/1-2-3",
		"/thirty-two-chars-exactly-1234567",
		"/settings/api-keys",
		"/users/active/recent",
	}
	for _, r := range valid {
		t.Run("ok"+r, func(t *testing.T) {
			m, _ := New(Config{ID: "demo"})
			m.RegisterUI(ModuleUI{
				DefaultPages: []UIPage{{Route: r, Title: "T", Export: "P"}},
			})
		})
	}
}

func TestRegisterUI_InvalidRoutePanics(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"empty":               "",
		"no-leading-slash":    "settings",
		"trailing-slash":      "/settings/",
		"uppercase":           "/Settings",
		"underscore":          "/settings_page",
		"trailing-hyphen":     "/settings-",
		"leading-hyphen":      "/-settings",
		"space":               "/settings page",
		"too-long-segment":    "/thirty-three-chars-just-over-limit",
		"reserved-underscore": "/_internal",
		"reserved-ms":         "/__ms",
		"reserved-ms-sub":     "/__ms-something",
		"double-slash":        "/settings//api-keys",
	}
	for name, route := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("expected panic for route %q", route)
				}
			}()
			m, _ := New(Config{ID: "demo"})
			m.RegisterUI(ModuleUI{
				DefaultPages: []UIPage{{Route: route, Title: "T", Export: "P"}},
			})
		})
	}
}

func TestRegisterUI_DuplicatePageRoutePanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("expected panic on duplicate page route")
		}
	}()
	m, _ := New(Config{ID: "demo"})
	m.RegisterUI(ModuleUI{
		DefaultPages: []UIPage{
			{Route: "/audit", Title: "A", Export: "A"},
			{Route: "/audit", Title: "B", Export: "B"},
		},
	})
}

// ---- RegisterUI: Surface field ----

func TestRegisterUI_DefaultSurfaceIsMain(t *testing.T) {
	t.Parallel()
	m, _ := New(Config{ID: "demo"})
	m.RegisterUI(ModuleUI{
		DefaultPages: []UIPage{
			{Route: "/", Title: "Home", Export: "mount"},
		},
	})
	ui := m.registry.UI()
	if ui.DefaultPages[0].Surface != UISurfaceMain {
		t.Errorf("expected empty/main Surface by default, got %q", ui.DefaultPages[0].Surface)
	}
}

func TestRegisterUI_AcceptsSettingsSurface(t *testing.T) {
	t.Parallel()
	m, _ := New(Config{ID: "demo"})
	m.RegisterUI(ModuleUI{
		DefaultPages: []UIPage{
			{Route: "/", Title: "Home", Export: "mount"},
			{Route: "/", Surface: UISurfaceSettings, Title: "Settings", Export: "mountSettings"},
		},
	})
	got := m.registry.UI().DefaultPages
	if len(got) != 2 {
		t.Fatalf("expected 2 pages, got %d", len(got))
	}
	if got[0].Surface != UISurfaceMain || got[1].Surface != UISurfaceSettings {
		t.Errorf("expected [main, settings], got [%q, %q]", got[0].Surface, got[1].Surface)
	}
}

func TestRegisterUI_UnknownSurfacePanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("expected panic on unknown Surface")
		}
	}()
	m, _ := New(Config{ID: "demo"})
	m.RegisterUI(ModuleUI{
		DefaultPages: []UIPage{
			{Route: "/", Surface: "bogus", Title: "X", Export: "X"},
		},
	})
}

func TestRegisterUI_DuplicateRouteAllowedAcrossSurfaces(t *testing.T) {
	// Same route ("/") on different surfaces is fine — each surface
	// has its own root and they mount under different platform shells.
	t.Parallel()
	m, _ := New(Config{ID: "demo"})
	m.RegisterUI(ModuleUI{
		DefaultPages: []UIPage{
			{Route: "/", Title: "Main Home", Export: "mount"},
			{Route: "/", Surface: UISurfaceSettings, Title: "Settings Home", Export: "mountSettings"},
		},
	})
	// no panic => pass
}

func TestRegisterUI_DuplicateRouteSameSurfacePanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("expected panic on duplicate (surface, route)")
		}
	}()
	m, _ := New(Config{ID: "demo"})
	m.RegisterUI(ModuleUI{
		DefaultPages: []UIPage{
			{Route: "/cfg", Surface: UISurfaceSettings, Title: "A", Export: "A"},
			{Route: "/cfg", Surface: UISurfaceSettings, Title: "B", Export: "B"},
		},
	})
}

// ---- RegisterUI: component validation ----

func TestRegisterUI_DuplicateComponentNamePanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("expected panic on duplicate Component name")
		}
	}()
	m, _ := New(Config{ID: "demo"})
	m.RegisterUI(ModuleUI{
		Components: []UIComponent{
			{Name: "X", Export: "X"},
			{Name: "X", Export: "Y"},
		},
	})
}

func TestRegisterUI_EmptyComponentFieldsPanic(t *testing.T) {
	t.Parallel()

	cases := map[string]UIComponent{
		"empty-name":   {Name: "", Export: "X"},
		"empty-export": {Name: "X", Export: ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("expected panic for %s", name)
				}
			}()
			m, _ := New(Config{ID: "demo"})
			m.RegisterUI(ModuleUI{Components: []UIComponent{c}})
		})
	}
}

func TestRegisterUI_EmptyPageFieldsPanic(t *testing.T) {
	t.Parallel()

	cases := map[string]UIPage{
		"empty-title":  {Route: "/", Title: "", Export: "P"},
		"empty-export": {Route: "/", Title: "T", Export: ""},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("expected panic for %s", name)
				}
			}()
			m, _ := New(Config{ID: "demo"})
			m.RegisterUI(ModuleUI{DefaultPages: []UIPage{p}})
		})
	}
}

// ---- RegisterUI: prop validation ----

func TestRegisterUI_ValidPropTypes(t *testing.T) {
	t.Parallel()

	allowed := []string{"text", "secret", "textarea", "bool", "number", "text-list"}
	props := make([]UIProp, len(allowed))
	for i, ty := range allowed {
		props[i] = UIProp{Key: "p" + ty, Type: ty}
	}
	m, _ := New(Config{ID: "demo"})
	m.RegisterUI(ModuleUI{
		Components: []UIComponent{{Name: "C", Export: "C", Props: props}},
	})
	got := m.registry.UI().Components[0].Props
	if len(got) != len(allowed) {
		t.Errorf("len(props) = %d, want %d", len(got), len(allowed))
	}
}

func TestRegisterUI_InvalidPropTypePanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("expected panic for unknown prop type")
		}
	}()
	m, _ := New(Config{ID: "demo"})
	m.RegisterUI(ModuleUI{
		Components: []UIComponent{
			{Name: "C", Export: "C", Props: []UIProp{{Key: "k", Type: "color"}}},
		},
	})
}

func TestRegisterUI_DuplicatePropKeyPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("expected panic for duplicate prop key")
		}
	}()
	m, _ := New(Config{ID: "demo"})
	m.RegisterUI(ModuleUI{
		Components: []UIComponent{
			{Name: "C", Export: "C", Props: []UIProp{
				{Key: "appId", Type: "text"},
				{Key: "appId", Type: "text"},
			}},
		},
	})
}

func TestRegisterUI_EmptyPropKeyPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("expected panic for empty prop key")
		}
	}()
	m, _ := New(Config{ID: "demo"})
	m.RegisterUI(ModuleUI{
		Components: []UIComponent{
			{Name: "C", Export: "C", Props: []UIProp{{Key: "", Type: "text"}}},
		},
	})
}

// ---- RegisterUI: end-to-end multi-page ----

func TestRegisterUI_MultiPage(t *testing.T) {
	t.Parallel()

	m, _ := New(Config{ID: "demo"})
	m.RegisterUI(ModuleUI{
		DefaultPages: []UIPage{
			{Route: "/", Title: "Settings", Export: "SettingsPage"},
			{Route: "/connections", Title: "Connections", Export: "ConnectionsPage"},
			{Route: "/audit", Title: "Audit log", Export: "AuditPage"},
		},
	})

	got := m.registry.UI().DefaultPages
	wantRoutes := []string{"/", "/connections", "/audit"}
	gotRoutes := make([]string, len(got))
	for i, p := range got {
		gotRoutes[i] = p.Route
	}
	if !slices.Equal(gotRoutes, wantRoutes) {
		t.Errorf("routes = %+v, want %+v", gotRoutes, wantRoutes)
	}
}
