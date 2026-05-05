package core

import (
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
)

func TestExposeView_Records(t *testing.T) {
	m, err := New(Config{ID: "media", Name: "Media"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m.ExposeView("recent_orders", "@anna/analytics")
	got := m.registry.Exposures()
	if len(got) != 1 {
		t.Fatalf("expected 1 exposure, got %d", len(got))
	}
	if got[0].Kind != registry.ExposureKindView {
		t.Errorf("kind = %q, want view", got[0].Kind)
	}
}

func TestExposeTable_RecordsKindTable(t *testing.T) {
	m, _ := New(Config{ID: "audit", Name: "Audit"})
	m.ExposeTable("entries", "@security/audit-collector")
	got := m.registry.Exposures()
	if got[0].Kind != registry.ExposureKindTable {
		t.Errorf("kind = %q, want table", got[0].Kind)
	}
}

func TestExposeView_VariadicReadersComposeIntoSlice(t *testing.T) {
	m, _ := New(Config{ID: "x", Name: "X"})
	m.ExposeView("links", "@anna/oauth-google", "@anna/oauth-github")
	got := m.registry.Exposures()
	if len(got[0].ReadableBy) != 2 {
		t.Errorf("expected 2 readers, got %+v", got[0].ReadableBy)
	}
}

func TestExposeView_NoReadersIsAllowed(t *testing.T) {
	// Empty readableBy = "no consumers declared yet" — valid placeholder.
	m, _ := New(Config{ID: "x", Name: "X"})
	m.ExposeView("internal_view")
	got := m.registry.Exposures()
	if len(got[0].ReadableBy) != 0 {
		t.Errorf("expected empty readers, got %+v", got[0].ReadableBy)
	}
}

func TestExposeView_Default_PanicsWithoutInit(t *testing.T) {
	resetDefault(t)
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when default module not initialized")
		}
	}()
	ExposeView("x", "@anna/y")
}

func TestExposeView_Default_RoutesToInitializedModule(t *testing.T) {
	resetDefault(t)
	if err := Init(Config{ID: "media", Name: "Media"}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	ExposeView("links", "@anna/dashboard")
	got := DefaultModule().registry.Exposures()
	if len(got) != 1 || got[0].Name != "links" {
		t.Errorf("default-module dispatch broken: %+v", got)
	}
}
