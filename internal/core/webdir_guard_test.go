package core

import (
	"strings"
	"testing"
)

// checkUIServable exists because of a real outage-shaped bug: a module declared
// a settings page, its manifest advertised it, Go built, the bundle built, CI
// passed and an adversarial review passed — and the page still failed in the
// browser with "Couldn't load module bundle", because Config.WebDir was empty
// so GET /__mirrorstack/web/* had no directory behind it.
//
// These tests pin the exact combination that is contradictory, and — just as
// importantly — the three that are not, so the guard can never start rejecting
// a legitimate module.

func TestCheckUIServable_DeclaredPagesWithoutWebDir(t *testing.T) {
	t.Parallel()

	m, _ := New(Config{ID: "demo"})
	m.RegisterUI(ModuleUI{
		DefaultPages: []UIPage{{Route: "/", Title: "Settings", Export: "mountSettings"}},
	})

	err := m.checkUIServable()
	if err == nil {
		t.Fatal("checkUIServable() = nil; want an error — a declared page with no WebDir 404s at runtime")
	}
	// The message has to name the fix, not just the symptom: this fires on a
	// developer's first run of a new surface, and "WebDir" is the one word
	// that turns it into a one-line change.
	for _, want := range []string{"WebDir", "web/dist", "__mirrorstack/web"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestCheckUIServable_DeclaredComponentsWithoutWebDir(t *testing.T) {
	t.Parallel()

	// Components are served from the same bundle as pages, so a
	// components-only surface is just as unservable.
	m, _ := New(Config{ID: "demo"})
	m.RegisterUI(ModuleUI{
		Components: []UIComponent{{Name: "Card", Export: "Card"}},
	})

	if err := m.checkUIServable(); err == nil {
		t.Fatal("checkUIServable() = nil for components-only UI with no WebDir; want an error")
	}
}

func TestCheckUIServable_Allowed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		webDir string
		ui     *ModuleUI
	}{
		{
			// The overwhelmingly common case: a headless module.
			name: "no UI and no WebDir",
		},
		{
			name:   "UI declared with WebDir",
			webDir: "web/dist",
			ui: &ModuleUI{
				DefaultPages: []UIPage{{Route: "/", Title: "Settings", Export: "mountSettings"}},
			},
		},
		{
			// WebDir without RegisterUI is legitimate — the module may serve
			// assets that another module's contributed surface renders.
			name:   "WebDir with no UI",
			webDir: "web/dist",
		},
		{
			// RegisterUI called with nothing in it declares no surface, so
			// there is nothing to fail to serve.
			name: "empty UI with no WebDir",
			ui:   &ModuleUI{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m, _ := New(Config{ID: "demo", WebDir: tt.webDir})
			if tt.ui != nil {
				m.RegisterUI(*tt.ui)
			}
			if err := m.checkUIServable(); err != nil {
				t.Errorf("checkUIServable() = %v; want nil", err)
			}
		})
	}
}
