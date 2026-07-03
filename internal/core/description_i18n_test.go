package core

import (
	"encoding/json"
	"testing"
	"testing/fstest"

	"github.com/mirrorstack-ai/app-module-sdk/i18n"
	"github.com/mirrorstack-ai/app-module-sdk/system"
)

// newModuleWithSecret builds a module after setting MS_INTERNAL_SECRET, so its
// /platform/manifest route (behind internalAuth) is reachable with the "secret"
// header. Unlike newTestModuleWithSecret it takes a full Config so a test can
// declare Description / DescriptionLabel.
func newModuleWithSecret(t *testing.T, cfg Config) *Module {
	t.Helper()
	t.Setenv("MS_INTERNAL_SECRET", "secret")
	m, err := New(cfg)
	if err != nil {
		t.Fatalf("New(%q): %v", cfg.ID, err)
	}
	return m
}

// manifestOf serves the module's platform manifest and decodes it. Mirrors the
// meter-label tests: the description-i18n path is asserted end-to-end through
// the real manifest endpoint, not the registry directly.
func manifestOf(t *testing.T, m *Module) system.ManifestPayload {
	t.Helper()
	rec := doRequestWithSecret(t, m.Router(), "GET", "/__mirrorstack/platform/manifest", "secret")
	var got system.ManifestPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	return got
}

// TestDescriptionLabel_LiteralSurfacesInManifest asserts a Config.DescriptionLabel
// built from a literal folds a per-locale descriptionLabels map into the manifest
// (mirroring the permission/metric label path: a literal resolves under the
// default locale), while the plain Description string stays set as the fallback.
func TestDescriptionLabel_LiteralSurfacesInManifest(t *testing.T) {
	m := newModuleWithSecret(t, Config{ID: "media", Description: "A demo module", DescriptionLabel: Text("A demo module")})
	got := manifestOf(t, m)
	if got.DescriptionLabels[i18n.DefaultLocale] != "A demo module" {
		t.Errorf("DescriptionLabels = %v, want %s=A demo module", got.DescriptionLabels, i18n.DefaultLocale)
	}
	if got.Description != "A demo module" {
		t.Errorf("Description = %q, want %q (plain string stays the fallback)", got.Description, "A demo module")
	}
}

// TestDescription_PlainLeavesLabelsAbsent asserts a plain-string Description (no
// DescriptionLabel) ships the description string but NO descriptionLabels key
// (omitempty), so the platform falls back to the string. This is the back-compat
// path — existing ms.Config.Description keeps working unchanged.
func TestDescription_PlainLeavesLabelsAbsent(t *testing.T) {
	m := newModuleWithSecret(t, Config{ID: "media", Description: "A demo module"})
	got := manifestOf(t, m)
	if got.DescriptionLabels != nil {
		t.Errorf("DescriptionLabels = %v, want nil (no label declared)", got.DescriptionLabels)
	}
	if got.Description != "A demo module" {
		t.Errorf("Description = %q, want %q", got.Description, "A demo module")
	}
}

// TestDescriptionLabel_ResolvesCatalogPerLocale asserts a DescriptionLabel built
// from an i18n catalog key (ms.T) resolves to every loaded locale at manifest
// build, exactly like a permission/metric Label — and, because resolution is
// lazy, it works even though RegisterMessages here runs AFTER New (the module's
// real Init → RegisterMessages ordering).
func TestDescriptionLabel_ResolvesCatalogPerLocale(t *testing.T) {
	i18n.Reset()
	t.Cleanup(i18n.Reset)

	m := newModuleWithSecret(t, Config{ID: "media", Description: "Manages sign-in", DescriptionLabel: T("description")})

	// Catalog registered AFTER New — lazy manifest-build resolution still picks
	// it up, so authors are free to load catalogs after Init.
	fsys := fstest.MapFS{
		"i18n/en-US.json": &fstest.MapFile{Data: []byte(`{"description":"Manages sign-in"}`)},
		"i18n/zh-TW.json": &fstest.MapFile{Data: []byte(`{"description":"管理登入"}`)},
	}
	if err := i18n.RegisterMessages(fsys, "i18n"); err != nil {
		t.Fatalf("RegisterMessages: %v", err)
	}

	got := manifestOf(t, m)
	if got.DescriptionLabels["en-US"] != "Manages sign-in" {
		t.Errorf("en-US = %q, want %q", got.DescriptionLabels["en-US"], "Manages sign-in")
	}
	if got.DescriptionLabels["zh-TW"] != "管理登入" {
		t.Errorf("zh-TW = %q, want %q", got.DescriptionLabels["zh-TW"], "管理登入")
	}
}
