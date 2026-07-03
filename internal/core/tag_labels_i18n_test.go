package core

import (
	"testing"
	"testing/fstest"

	"github.com/mirrorstack-ai/app-module-sdk/i18n"
)

// TestTagLabels_ResolveCatalogPerLocale asserts that a module registering the
// "module.name" and "module.tags" catalog keys surfaces BOTH nameLabels and
// tagLabels per-locale in its manifest, while the raw Config.Name/Tags stay as
// the non-localized fallback. TagLabels is the list counterpart of NameLabels:
// the author packs each locale's tag list into a single comma-separated
// catalog value (en-US "Auth", zh-TW "驗證") and the manifest splits it.
func TestTagLabels_ResolveCatalogPerLocale(t *testing.T) {
	i18n.Reset()
	t.Cleanup(i18n.Reset)

	m := newModuleWithSecret(t, Config{ID: "oauth", Name: "OAuth Core", Tags: []string{"Auth"}})

	// Catalog registered AFTER New — Lookup/LookupList read the global catalog
	// lazily at manifest build, so catalog-load ordering is free (mirrors the
	// description-label path).
	fsys := fstest.MapFS{
		"i18n/en-US.json": &fstest.MapFile{Data: []byte(`{"module":{"name":"OAuth Core","tags":"Auth"}}`)},
		"i18n/zh-TW.json": &fstest.MapFile{Data: []byte(`{"module":{"name":"OAuth 核心","tags":"驗證"}}`)},
	}
	if err := i18n.RegisterMessages(fsys, "i18n"); err != nil {
		t.Fatalf("RegisterMessages: %v", err)
	}

	got := manifestOf(t, m)

	// Raw defaults remain the fallback.
	if got.Defaults.Name != "OAuth Core" {
		t.Errorf("Defaults.Name = %q, want %q (raw stays the fallback)", got.Defaults.Name, "OAuth Core")
	}
	if len(got.Defaults.Tags) != 1 || got.Defaults.Tags[0] != "Auth" {
		t.Errorf("Defaults.Tags = %v, want [Auth] (raw stays the fallback)", got.Defaults.Tags)
	}

	// NameLabels per-locale.
	if got.Defaults.NameLabels["en-US"] != "OAuth Core" {
		t.Errorf("NameLabels[en-US] = %q, want %q", got.Defaults.NameLabels["en-US"], "OAuth Core")
	}
	if got.Defaults.NameLabels["zh-TW"] != "OAuth 核心" {
		t.Errorf("NameLabels[zh-TW] = %q, want %q", got.Defaults.NameLabels["zh-TW"], "OAuth 核心")
	}

	// TagLabels per-locale (list-valued).
	if want := []string{"Auth"}; !equalTagList(got.Defaults.TagLabels["en-US"], want) {
		t.Errorf("TagLabels[en-US] = %v, want %v", got.Defaults.TagLabels["en-US"], want)
	}
	if want := []string{"驗證"}; !equalTagList(got.Defaults.TagLabels["zh-TW"], want) {
		t.Errorf("TagLabels[zh-TW] = %v, want %v", got.Defaults.TagLabels["zh-TW"], want)
	}
}

// TestTagLabels_AbsentWhenNoCatalog asserts a module with NO module.name /
// module.tags catalog keys ships neither nameLabels nor tagLabels (both omitted
// via omitempty), so the platform falls back to the raw Name/Tags. This is the
// back-compat path — a plain-string module keeps working unchanged.
func TestTagLabels_AbsentWhenNoCatalog(t *testing.T) {
	i18n.Reset()
	t.Cleanup(i18n.Reset)

	m := newModuleWithSecret(t, Config{ID: "oauth", Name: "OAuth Core", Tags: []string{"Auth"}})
	got := manifestOf(t, m)

	if got.Defaults.NameLabels != nil {
		t.Errorf("NameLabels = %v, want nil (no catalog declared)", got.Defaults.NameLabels)
	}
	if got.Defaults.TagLabels != nil {
		t.Errorf("TagLabels = %v, want nil (no catalog declared)", got.Defaults.TagLabels)
	}
	if len(got.Defaults.Tags) != 1 || got.Defaults.Tags[0] != "Auth" {
		t.Errorf("Defaults.Tags = %v, want [Auth] (raw fallback)", got.Defaults.Tags)
	}
}

func equalTagList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
