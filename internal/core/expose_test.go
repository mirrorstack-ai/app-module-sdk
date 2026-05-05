package core

import (
	"testing"
)

func TestExposeTable_Records(t *testing.T) {
	m, err := New(Config{ID: "media", Name: "Media"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m.ExposeTable("recent_orders", "@anna/analytics")
	got := m.registry.Exposures()
	if len(got) != 1 {
		t.Fatalf("expected 1 exposure, got %d", len(got))
	}
	if got[0].Name != "recent_orders" {
		t.Errorf("name = %q, want recent_orders", got[0].Name)
	}
}

func TestExposeTable_VariadicReadersComposeIntoSlice(t *testing.T) {
	m, _ := New(Config{ID: "x", Name: "X"})
	m.ExposeTable("links", "@anna/oauth-google", "@anna/oauth-github")
	got := m.registry.Exposures()
	if len(got[0].ReadableBy) != 2 {
		t.Errorf("expected 2 readers, got %+v", got[0].ReadableBy)
	}
}

func TestExposeTable_NoReadersIsAllowed(t *testing.T) {
	// Empty readableBy = "no consumers declared yet" — valid placeholder.
	m, _ := New(Config{ID: "x", Name: "X"})
	m.ExposeTable("internal_view")
	got := m.registry.Exposures()
	if len(got[0].ReadableBy) != 0 {
		t.Errorf("expected empty readers, got %+v", got[0].ReadableBy)
	}
}

func TestExposeTable_Default_PanicsWithoutInit(t *testing.T) {
	resetDefault(t)
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when default module not initialized")
		}
	}()
	ExposeTable("x", "@anna/y")
}

func TestExposeTable_Default_RoutesToInitializedModule(t *testing.T) {
	resetDefault(t)
	if err := Init(Config{ID: "media", Name: "Media"}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	ExposeTable("links", "@anna/dashboard")
	got := DefaultModule().registry.Exposures()
	if len(got) != 1 || got[0].Name != "links" {
		t.Errorf("default-module dispatch broken: %+v", got)
	}
}
