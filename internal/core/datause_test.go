package core

import (
	"context"
	"strings"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/system"
)

func TestConfigDataUse_SurfacesInManifestAndValidates(t *testing.T) {
	resetDefault(t)
	m := newModuleWithSecret(t, Config{ID: "demo", Name: "Demo", DataUse: &DataUse{
		Categories: []string{"usage", "contact"}, Retention: "6m", Hosts: []string{"api.example.com"},
	}})
	defaultModule = m
	MCPTool("find", "Find", func(_ context.Context, a greetArgs) (greetResult, error) {
		return greetResult{}, nil
	}, ToolPersonalData("contact", "contact"))

	rec := doRequestWithSecret(t, m.Router(), "GET", "/__mirrorstack/platform/manifest", "secret")
	sum, err := system.ValidateManifestDataUse(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("manifest rejected: %v", err)
	}
	want := "dataUse: categories=[contact, usage] retention=6m hosts=[api.example.com]"
	if sum.String() != want {
		t.Errorf("summary = %q, want %q", sum.String(), want)
	}
	if got := sum.ToolCategories["find"]; len(got) != 1 || got[0] != "contact" {
		t.Errorf("tool categories = %v, want [contact] (deduped)", sum.ToolCategories)
	}
}

func TestConfigDataUse_AbsentWhenUndeclared(t *testing.T) {
	resetDefault(t)
	m := newModuleWithSecret(t, Config{ID: "demo", Name: "Demo"})
	if manifestOf(t, m).DataUse != nil {
		t.Error("a module with no Config.DataUse must ship no dataUse key")
	}
	rec := doRequestWithSecret(t, m.Router(), "GET", "/__mirrorstack/platform/manifest", "secret")
	if _, err := system.ValidateManifestDataUse(rec.Body.Bytes()); err == nil {
		t.Error("validator accepted a manifest with no dataUse")
	}
}

func TestConfigDataUse_InvalidPanicsAtNew(t *testing.T) {
	resetDefault(t)
	t.Setenv("MS_INTERNAL_SECRET", "secret")
	defer func() {
		r := recover()
		if s, _ := r.(string); !strings.Contains(s, "Config.DataUse") {
			t.Errorf("recover = %v, want a Config.DataUse panic", r)
		}
	}()
	_, _ = New(Config{ID: "demo", Name: "Demo", DataUse: &DataUse{Categories: []string{"pii"}, Retention: "1y"}})
}

func TestToolPersonalData_UnknownCategoryPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("ToolPersonalData accepted an unknown category")
		}
	}()
	ToolPersonalData("pii")
}
