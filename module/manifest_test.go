package module

import "testing"

func TestModuleManifestZeroValue(t *testing.T) {
	var m ModuleManifest
	if m.ID != "" {
		t.Errorf("zero-value ID should be empty, got %q", m.ID)
	}
	if m.Dependencies != nil {
		t.Error("zero-value Dependencies should be nil")
	}
	if m.Contributions != nil {
		t.Error("zero-value Contributions should be nil")
	}
	if m.DefaultStrings != nil {
		t.Error("zero-value DefaultStrings should be nil")
	}
}

func TestModuleManifestPopulated(t *testing.T) {
	m := ModuleManifest{
		ID:             "test-module",
		NameKey:        "module.test.name",
		DescriptionKey: "module.test.description",
		Icon:           "test_icon",
		Category:       "testing",
		Version:        "1.0.0",
		Dependencies:   []string{"core"},
		NavItems: []NavItem{
			{Icon: "nav", Label: "Nav", Route: "/nav"},
		},
		QuickActions: []NavItem{
			{Icon: "action", Label: "Action", Route: "/action"},
		},
		SettingsPages: []NavItem{
			{Icon: "settings", Label: "Settings", Route: "/settings"},
		},
		Contributions: map[string][]Contribution{
			"dashboard": {
				{ID: "widget-1", Title: "Widget", Component: "TestWidget"},
			},
		},
		DefaultStrings: map[string]string{
			"module.test.name": "Test Module",
		},
	}

	tests := []struct {
		name string
		got  string
		want string
	}{
		{"ID", m.ID, "test-module"},
		{"NameKey", m.NameKey, "module.test.name"},
		{"DescriptionKey", m.DescriptionKey, "module.test.description"},
		{"Icon", m.Icon, "test_icon"},
		{"Category", m.Category, "testing"},
		{"Version", m.Version, "1.0.0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("%s = %q, want %q", tt.name, tt.got, tt.want)
			}
		})
	}

	if len(m.Dependencies) != 1 || m.Dependencies[0] != "core" {
		t.Errorf("Dependencies = %v, want [core]", m.Dependencies)
	}
	if len(m.NavItems) != 1 {
		t.Fatalf("NavItems length = %d, want 1", len(m.NavItems))
	}
	if m.NavItems[0].Route != "/nav" {
		t.Errorf("NavItems[0].Route = %q, want /nav", m.NavItems[0].Route)
	}
	if len(m.QuickActions) != 1 {
		t.Fatalf("QuickActions length = %d, want 1", len(m.QuickActions))
	}
	if len(m.SettingsPages) != 1 {
		t.Fatalf("SettingsPages length = %d, want 1", len(m.SettingsPages))
	}

	cards, ok := m.Contributions["dashboard"]
	if !ok {
		t.Fatal("expected contributions for dashboard")
	}
	if len(cards) != 1 || cards[0].Component != "TestWidget" {
		t.Errorf("Contributions[dashboard] = %v, want [{widget-1 Widget TestWidget}]", cards)
	}

	if val := m.DefaultStrings["module.test.name"]; val != "Test Module" {
		t.Errorf("DefaultStrings[module.test.name] = %q, want %q", val, "Test Module")
	}
}

func TestNavItemFields(t *testing.T) {
	n := NavItem{Icon: "home", Label: "Home", Route: "/home"}
	if n.Icon != "home" || n.Label != "Home" || n.Route != "/home" {
		t.Errorf("NavItem fields mismatch: %+v", n)
	}
}

func TestContributionFields(t *testing.T) {
	c := Contribution{ID: "c1", Title: "Card", Component: "CardComponent"}
	if c.ID != "c1" || c.Title != "Card" || c.Component != "CardComponent" {
		t.Errorf("Contribution fields mismatch: %+v", c)
	}
}
