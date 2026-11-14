package system

import (
	"errors"
	"strings"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
)

func manifestBytes(t *testing.T, reg *registry.Registry) []byte {
	t.Helper()
	doc, err := BuildManifest("media", "", "Media", "perm_media", nil, nil, nil, reg, nil, nil)
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	return doc.Body
}

func TestManifest_DataUseOnPayload(t *testing.T) {
	t.Parallel()
	reg := registry.New()
	reg.SetDataUse(registry.DataUse{Categories: []string{"identity", "contact"}, Retention: "90d"})
	got := decodeManifest(t, ManifestHandler("media", "", "Media", "i", nil, nil, nil, reg, nil))
	if got.DataUse == nil {
		t.Fatal("ManifestPayload.DataUse is nil, want the declared dataUse")
	}
	// Sorted, and hosts is [] not null, so the wire shape is stable.
	if strings.Join(got.DataUse.Categories, ",") != "contact,identity" || got.DataUse.Hosts == nil {
		t.Errorf("dataUse = %+v", got.DataUse)
	}
}

func TestValidateManifestDataUse_RejectsMissing(t *testing.T) {
	t.Parallel()
	_, err := ValidateManifestDataUse(manifestBytes(t, registry.New()))
	if !errors.Is(err, ErrNoDataUse) {
		t.Fatalf("err = %v, want ErrNoDataUse", err)
	}
}

func TestValidateManifestDataUse_PrintsCategoriesAndHosts(t *testing.T) {
	t.Parallel()
	reg := registry.New()
	reg.SetDataUse(registry.DataUse{
		Categories: []string{"identity", "content"}, Retention: "1y",
		Hosts: []string{"api.stripe.com", "hooks.example.org"},
	})
	sum, err := ValidateManifestDataUse(manifestBytes(t, reg))
	if err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	want := "dataUse: categories=[content, identity] retention=1y hosts=[api.stripe.com, hooks.example.org]"
	if sum.String() != want {
		t.Errorf("String() = %q, want %q", sum.String(), want)
	}
}

func TestValidateManifestDataUse_NoHostsPrintsNone(t *testing.T) {
	t.Parallel()
	reg := registry.New()
	reg.SetDataUse(registry.DataUse{Categories: []string{"none"}, Retention: "none"})
	sum, err := ValidateManifestDataUse(manifestBytes(t, reg))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(sum.String(), "hosts=[none]") {
		t.Errorf("String() = %q", sum.String())
	}
}

func TestValidateManifestDataUse_RejectsBadValues(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"empty categories": `{"dataUse":{"categories":[],"retention":"1y","hosts":[]}}`,
		"unknown category": `{"dataUse":{"categories":["pii"],"retention":"1y","hosts":[]}}`,
		"none mixed":       `{"dataUse":{"categories":["none","usage"],"retention":"1y","hosts":[]}}`,
		"dup category":     `{"dataUse":{"categories":["usage","usage"],"retention":"1y","hosts":[]}}`,
		"bad retention":    `{"dataUse":{"categories":["usage"],"retention":"forever","hosts":[]}}`,
		"zero retention":   `{"dataUse":{"categories":["usage"],"retention":"0d","hosts":[]}}`,
		"url host":         `{"dataUse":{"categories":["usage"],"retention":"1y","hosts":["https://x.com"]}}`,
		"wildcard host":    `{"dataUse":{"categories":["usage"],"retention":"1y","hosts":["*.x.com"]}}`,
		"port host":        `{"dataUse":{"categories":["usage"],"retention":"1y","hosts":["x.com:443"]}}`,
		"tool outside":     `{"dataUse":{"categories":["usage"],"retention":"1y","hosts":[]},"mcp":{"tools":[{"name":"t","personalData":["contact"]}]}}`,
		"tool none mixed":  `{"dataUse":{"categories":["usage"],"retention":"1y","hosts":[]},"mcp":{"tools":[{"name":"t","personalData":["none","usage"]}]}}`,
		"not json":         `nope`,
	}
	for name, body := range cases {
		if _, err := ValidateManifestDataUse([]byte(body)); err == nil {
			t.Errorf("%s: accepted, want error", name)
		}
	}
}

func TestValidateManifestDataUse_ToolCategories(t *testing.T) {
	t.Parallel()
	body := `{"dataUse":{"categories":["usage","contact"],"retention":"30d","hosts":[]},"mcp":{"tools":[{"name":"find","personalData":["contact"]},{"name":"ping"}]}}`
	sum, err := ValidateManifestDataUse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if got := sum.ToolCategories["find"]; len(got) != 1 || got[0] != "contact" {
		t.Errorf("ToolCategories = %v", sum.ToolCategories)
	}
	if _, ok := sum.ToolCategories["ping"]; ok {
		t.Error("tool with no declaration must be absent")
	}
}
