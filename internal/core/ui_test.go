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
			{Slug: "", Title: "OAuth Settings", Export: "SettingsPage"},
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
		DefaultPages: []UIPage{{Slug: "", Title: "First", Export: "First"}},
	})
	m.RegisterUI(ModuleUI{
		DefaultPages: []UIPage{{Slug: "", Title: "Second", Export: "Second"}},
	})

	got := m.registry.UI()
	if len(got.DefaultPages) != 1 || got.DefaultPages[0].Title != "Second" {
		t.Errorf("after second RegisterUI, DefaultPages = %+v, want only [{Second}]", got.DefaultPages)
	}
}

func TestRegisterUI_StoresDeepCopy(t *testing.T) {
	t.Parallel()

	// Mutating the input after RegisterUI must not corrupt the registry.
	m, _ := New(Config{ID: "demo"})
	pages := []UIPage{{Slug: "", Title: "Original", Export: "P"}}
	m.RegisterUI(ModuleUI{DefaultPages: pages})

	pages[0].Title = "Mutated"

	got := m.registry.UI()
	if got.DefaultPages[0].Title != "Original" {
		t.Errorf("registry mutated through input slice: Title = %q", got.DefaultPages[0].Title)
	}
}

func TestRegisterUI_UIReturnsDeepCopy(t *testing.T) {
	t.Parallel()

	// Mutating the returned manifest must not corrupt the registry.
	m, _ := New(Config{ID: "demo"})
	m.RegisterUI(ModuleUI{
		DefaultPages: []UIPage{{Slug: "", Title: "Original", Export: "P"}},
	})

	first := m.registry.UI()
	first.DefaultPages[0].Title = "Mutated"

	second := m.registry.UI()
	if second.DefaultPages[0].Title != "Original" {
		t.Errorf("registry mutated through UI() return: Title = %q", second.DefaultPages[0].Title)
	}
}

// ---- RegisterUI: page slug validation ----

func TestRegisterUI_EmptySlugIsIndex(t *testing.T) {
	t.Parallel()

	// Empty slug is the index page — not subject to the regex.
	m, _ := New(Config{ID: "demo"})
	m.RegisterUI(ModuleUI{
		DefaultPages: []UIPage{{Slug: "", Title: "Index", Export: "P"}},
	})
}

func TestRegisterUI_ValidSlugVariations(t *testing.T) {
	t.Parallel()

	valid := []string{"a", "1", "settings", "audit-log", "v2-api", "abc123", "1-2-3",
		"thirty-two-chars-exactly-1234567"}
	for _, s := range valid {
		t.Run("ok/"+s, func(t *testing.T) {
			m, _ := New(Config{ID: "demo"})
			m.RegisterUI(ModuleUI{
				DefaultPages: []UIPage{{Slug: s, Title: "T", Export: "P"}},
			})
		})
	}
}

func TestRegisterUI_InvalidSlugPanics(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"uppercase":           "Settings",
		"underscore":          "settings_page",
		"trailing-hyphen":     "settings-",
		"leading-hyphen":      "-settings",
		"space":               "settings page",
		"too-long":            "thirty-three-chars-just-over-limit",
		"reserved-underscore": "_internal",
		"reserved-ms":         "__ms",
		"reserved-ms-sub":     "__ms-something",
	}
	for name, slug := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("expected panic for slug %q", slug)
				}
			}()
			m, _ := New(Config{ID: "demo"})
			m.RegisterUI(ModuleUI{
				DefaultPages: []UIPage{{Slug: slug, Title: "T", Export: "P"}},
			})
		})
	}
}

func TestRegisterUI_DuplicatePageSlugPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("expected panic on duplicate page slug")
		}
	}()
	m, _ := New(Config{ID: "demo"})
	m.RegisterUI(ModuleUI{
		DefaultPages: []UIPage{
			{Slug: "audit", Title: "A", Export: "A"},
			{Slug: "audit", Title: "B", Export: "B"},
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
		"empty-title":  {Slug: "", Title: "", Export: "P"},
		"empty-export": {Slug: "", Title: "T", Export: ""},
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
			{Slug: "", Icon: "settings", Title: "Settings", Export: "SettingsPage"},
			{Slug: "connections", Icon: "link", Title: "Connections", Export: "ConnectionsPage"},
			{Slug: "audit", Icon: "history", Title: "Audit log", Export: "AuditPage"},
		},
	})

	got := m.registry.UI().DefaultPages
	wantSlugs := []string{"", "connections", "audit"}
	gotSlugs := make([]string, len(got))
	for i, p := range got {
		gotSlugs[i] = p.Slug
	}
	if !slices.Equal(gotSlugs, wantSlugs) {
		t.Errorf("slugs = %+v, want %+v", gotSlugs, wantSlugs)
	}
}
