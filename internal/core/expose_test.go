package core

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/system"
)

func TestExposeTable_RecordsInRegistry(t *testing.T) {
	t.Parallel()

	m, _ := New(Config{ID: "media"})
	m.ExposeTable("orders")
	m.ExposeTable("invoices")
	m.ExposeTable("orders") // dup, dropped

	got := m.registry.ExposedTables()
	if !slices.Equal(got, []string{"invoices", "orders"}) {
		t.Errorf("ExposedTables() = %v, want sorted [invoices orders]", got)
	}
}

func TestExposeTable_PanicsOnInvalidName(t *testing.T) {
	t.Parallel()

	m, _ := New(Config{ID: "media"})
	assertPanics(t, "expected panic on invalid exposed table name", func() {
		m.ExposeTable("")
	})
}

func TestExposeTable_AppearsInManifestExposes(t *testing.T) {
	m := newTestModuleWithSecret(t, "media")

	m.ExposeTable("orders")
	m.ExposeTable("invoices")

	rec := doRequestWithSecret(t, m.Router(), "GET", "/__mirrorstack/platform/manifest", "secret")
	var got system.ManifestPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	if !slices.Equal(got.Exposes.Tables, []string{"invoices", "orders"}) {
		t.Errorf("exposes.tables = %v, want sorted [invoices orders]", got.Exposes.Tables)
	}
}

func TestExposeTable_TopLevelFacade(t *testing.T) {
	resetDefault(t)
	if err := Init(Config{ID: "media", Name: "Media"}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	ExposeTable("orders")
	ExposeTable("invoices")

	got := DefaultModule().registry.ExposedTables()
	if !slices.Equal(got, []string{"invoices", "orders"}) {
		t.Errorf("package-level ExposeTable -> ExposedTables() = %v, want sorted [invoices orders]", got)
	}
}

func TestExposeTable_TopLevelPanicsBeforeInit(t *testing.T) {
	resetDefault(t)
	assertPanics(t, "expected panic for top-level ExposeTable before Init", func() {
		ExposeTable("orders")
	})
}
