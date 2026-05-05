package core

import (
	"testing"
)

func TestExposeTable_Records(t *testing.T) {
	m, err := New(Config{ID: "media", Name: "Media"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m.ExposeTable("recent_orders")
	got := m.registry.Exposures()
	if len(got) != 1 || got[0].Name != "recent_orders" {
		t.Fatalf("expected [recent_orders], got %+v", got)
	}
}

func TestExposeTable_FirstWinsOnDuplicate(t *testing.T) {
	m, _ := New(Config{ID: "x"})
	m.ExposeTable("orders")
	m.ExposeTable("orders") // duplicate — no-op
	got := m.registry.Exposures()
	if len(got) != 1 {
		t.Errorf("expected duplicate to collapse to 1 entry, got %d: %+v", len(got), got)
	}
}

func TestExposeTable_PreservesRegistrationOrder(t *testing.T) {
	m, _ := New(Config{ID: "x"})
	m.ExposeTable("a")
	m.ExposeTable("b")
	m.ExposeTable("c")
	got := m.registry.Exposures()
	if len(got) != 3 || got[0].Name != "a" || got[1].Name != "b" || got[2].Name != "c" {
		t.Errorf("registration order broken: %+v", got)
	}
}

func TestExposeTable_Default_PanicsWithoutInit(t *testing.T) {
	resetDefault(t)
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when default module not initialized")
		}
	}()
	ExposeTable("x")
}

func TestExposeTable_Default_RoutesToInitializedModule(t *testing.T) {
	resetDefault(t)
	if err := Init(Config{ID: "media"}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	ExposeTable("links")
	got := DefaultModule().registry.Exposures()
	if len(got) != 1 || got[0].Name != "links" {
		t.Errorf("default-module dispatch broken: %+v", got)
	}
}
