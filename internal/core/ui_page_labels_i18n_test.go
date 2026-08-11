package core

import (
	"maps"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/mirrorstack-ai/app-module-sdk/i18n"
)

func registerUIPageLabelTestPages(m *Module) {
	m.RegisterUI(ModuleUI{DefaultPages: []UIPage{
		{Route: "/", Title: "Videos", Export: "VideosPage"},
		{Route: "/sessions", Title: "Sessions", Export: "SessionsPage"},
		{
			Route:       "/",
			Surface:     UISurfaceSettings,
			Title:       "Video Core",
			Description: "Configure video categories and playback defaults.",
			Export:      "VideoSettingsPage",
		},
	}})
}

func TestUIPageLabels_ResolveCatalogPerLocale(t *testing.T) {
	i18n.Reset()
	t.Cleanup(i18n.Reset)

	m := newModuleWithSecret(t, Config{ID: "video", Name: "Video"})
	registerUIPageLabelTestPages(m)

	fsys := fstest.MapFS{
		"i18n/en-US.json": &fstest.MapFile{Data: []byte(`{"ui":{"pages":{"main":{"/":{"title":"Videos"},"/sessions":{"title":"Sessions"}},"settings":{"/":{"title":"Video Core","description":"Configure video categories and playback defaults."}}}}}`)},
		"i18n/zh-TW.json": &fstest.MapFile{Data: []byte(`{"ui":{"pages":{"main":{"/":{"title":"影片"},"/sessions":{"title":"工作階段"}},"settings":{"/":{"title":"影片核心","description":"設定影片分類與播放預設值。"}}}}}`)},
	}
	if err := i18n.RegisterMessages(fsys, "i18n"); err != nil {
		t.Fatalf("RegisterMessages: %v", err)
	}

	got := manifestOf(t, m)
	if got.UI == nil {
		t.Fatal("manifest UI is nil")
	}
	pages := got.UI.DefaultPages
	if len(pages) != 3 {
		t.Fatalf("DefaultPages len = %d, want 3", len(pages))
	}

	wantTitles := []string{"Videos", "Sessions", "Video Core"}
	wantZhTW := []string{"影片", "工作階段", "影片核心"}
	for i := range pages {
		if pages[i].Title != wantTitles[i] {
			t.Errorf("DefaultPages[%d].Title = %q, want %q (raw stays the fallback)", i, pages[i].Title, wantTitles[i])
		}
		if pages[i].TitleLabels["zh-TW"] != wantZhTW[i] {
			t.Errorf("DefaultPages[%d].TitleLabels[zh-TW] = %q, want %q", i, pages[i].TitleLabels["zh-TW"], wantZhTW[i])
		}
	}

	if maps.Equal(pages[0].TitleLabels, pages[2].TitleLabels) {
		t.Errorf("main / and settings / TitleLabels collided: %v", pages[0].TitleLabels)
	}
	if pages[0].DescriptionLabels != nil || pages[1].DescriptionLabels != nil {
		t.Errorf("main-page DescriptionLabels = %v, %v; want both nil", pages[0].DescriptionLabels, pages[1].DescriptionLabels)
	}
	if pages[2].DescriptionLabels["zh-TW"] != "設定影片分類與播放預設值。" {
		t.Errorf("settings DescriptionLabels[zh-TW] = %q, want %q", pages[2].DescriptionLabels["zh-TW"], "設定影片分類與播放預設值。")
	}
}

func TestUIPageLabels_AbsentWhenNoCatalog(t *testing.T) {
	i18n.Reset()
	t.Cleanup(i18n.Reset)

	m := newModuleWithSecret(t, Config{ID: "video", Name: "Video"})
	registerUIPageLabelTestPages(m)
	got := manifestOf(t, m)
	if got.UI == nil {
		t.Fatal("manifest UI is nil")
	}

	wantTitles := []string{"Videos", "Sessions", "Video Core"}
	for i, page := range got.UI.DefaultPages {
		if page.TitleLabels != nil {
			t.Errorf("DefaultPages[%d].TitleLabels = %v, want nil", i, page.TitleLabels)
		}
		if page.DescriptionLabels != nil {
			t.Errorf("DefaultPages[%d].DescriptionLabels = %v, want nil", i, page.DescriptionLabels)
		}
		if page.Title != wantTitles[i] {
			t.Errorf("DefaultPages[%d].Title = %q, want %q (raw fallback)", i, page.Title, wantTitles[i])
		}
	}
}

func TestUIPageLabels_EmptyTranslationIsCarriedVerbatim(t *testing.T) {
	i18n.Reset()
	t.Cleanup(i18n.Reset)

	m := newModuleWithSecret(t, Config{ID: "video", Name: "Video"})
	registerUIPageLabelTestPages(m)
	fsys := fstest.MapFS{
		"i18n/en-US.json": &fstest.MapFile{Data: []byte(`{"ui":{"pages":{"main":{"/":{"title":"Videos"}}}}}`)},
		"i18n/zh-TW.json": &fstest.MapFile{Data: []byte(`{"ui":{"pages":{"main":{"/":{"title":""}}}}}`)},
	}
	if err := i18n.RegisterMessages(fsys, "i18n"); err != nil {
		t.Fatalf("RegisterMessages: %v", err)
	}

	got := manifestOf(t, m)
	if got.UI == nil {
		t.Fatal("manifest UI is nil")
	}
	labels := got.UI.DefaultPages[0].TitleLabels
	if len(labels) != 2 {
		t.Fatalf("TitleLabels = %v, want both en-US and zh-TW", labels)
	}
	if v, ok := labels["en-US"]; !ok || v != "Videos" {
		t.Errorf("TitleLabels[en-US] = %q, present %v; want %q", v, ok, "Videos")
	}
	// Consumers must treat this as missing via || fallback, not ?? fallback.
	if v, ok := labels["zh-TW"]; !ok || v != "" {
		t.Errorf("TitleLabels[zh-TW] = %q, present %v; want present empty value", v, ok)
	}
}

func TestUIPageLabels_RawJSONCarriesTitleLabels(t *testing.T) {
	i18n.Reset()
	t.Cleanup(i18n.Reset)

	t.Run("catalog", func(t *testing.T) {
		fsys := fstest.MapFS{
			"i18n/en-US.json": &fstest.MapFile{Data: []byte(`{"ui":{"pages":{"main":{"/":{"title":"Videos"}}}}}`)},
			"i18n/zh-TW.json": &fstest.MapFile{Data: []byte(`{"ui":{"pages":{"main":{"/":{"title":"影片"}}}}}`)},
		}
		if err := i18n.RegisterMessages(fsys, "i18n"); err != nil {
			t.Fatalf("RegisterMessages: %v", err)
		}

		m := newModuleWithSecret(t, Config{ID: "video", Name: "Video"})
		registerUIPageLabelTestPages(m)
		rec := doRequestWithSecret(t, m.Router(), "GET", "/__mirrorstack/platform/manifest", "secret")
		if rec.Code != 200 {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"titleLabels":{`) {
			t.Errorf("raw manifest lacks titleLabels object: %s", body)
		}
		if !strings.Contains(body, "影片") {
			t.Errorf("raw manifest lacks zh-TW title: %s", body)
		}
	})

	t.Run("no catalog", func(t *testing.T) {
		i18n.Reset()
		m := newModuleWithSecret(t, Config{ID: "video", Name: "Video"})
		registerUIPageLabelTestPages(m)
		rec := doRequestWithSecret(t, m.Router(), "GET", "/__mirrorstack/platform/manifest", "secret")
		if rec.Code != 200 {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if body := rec.Body.String(); strings.Contains(body, "titleLabels") {
			t.Errorf("raw manifest contains titleLabels without catalog: %s", body)
		}
	})
}
