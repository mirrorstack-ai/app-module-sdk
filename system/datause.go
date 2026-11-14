package system

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
)

// ErrNoDataUse is returned by ValidateManifestDataUse for a manifest that
// carries no "dataUse" declaration.
var ErrNoDataUse = errors.New("manifest has no dataUse: declare Config.DataUse (personal-data categories, retention, outside hosts)")

// DataUseSummary is what a valid manifest declares, for the CLI and the
// platform gate to print or store.
type DataUseSummary struct {
	Categories []string
	Retention  string
	Hosts      []string
	// ToolCategories maps a tool name to the categories it declared with
	// ms.ToolPersonalData. Tools that declared none are absent.
	ToolCategories map[string][]string
}

// String renders the one-line form the CLI prints for a valid manifest.
func (s DataUseSummary) String() string {
	hosts := "none"
	if len(s.Hosts) > 0 {
		hosts = strings.Join(s.Hosts, ", ")
	}
	return fmt.Sprintf("dataUse: categories=[%s] retention=%s hosts=[%s]",
		strings.Join(s.Categories, ", "), s.Retention, hosts)
}

// ValidateManifestDataUse checks the dataUse declaration in a served manifest
// body (the bytes of GET /__mirrorstack/platform/manifest, or a release
// envelope's manifest). It is the single rule set behind `mirrorstack` CLI
// validation and the platform's finalize/approve gate (PV17):
//
//   - no dataUse                      -> ErrNoDataUse
//   - bad category, retention or host -> the registry.DataUse.Validate error
//   - a tool declaring a category the module did not -> error naming the tool
//   - a tool declaring "none" next to another category -> error
func ValidateManifestDataUse(manifest []byte) (DataUseSummary, error) {
	var m struct {
		DataUse *registry.DataUse `json:"dataUse"`
		MCP     struct {
			Tools []struct {
				Name         string   `json:"name"`
				PersonalData []string `json:"personalData"`
			} `json:"tools"`
		} `json:"mcp"`
	}
	if err := json.NewDecoder(bytes.NewReader(manifest)).Decode(&m); err != nil {
		return DataUseSummary{}, fmt.Errorf("manifest is not valid JSON: %w", err)
	}
	if m.DataUse == nil {
		return DataUseSummary{}, ErrNoDataUse
	}
	if err := m.DataUse.Validate(); err != nil {
		return DataUseSummary{}, err
	}
	sum := DataUseSummary{
		Categories:     slices.Clone(m.DataUse.Categories),
		Retention:      m.DataUse.Retention,
		Hosts:          slices.Clone(m.DataUse.Hosts),
		ToolCategories: map[string][]string{},
	}
	slices.Sort(sum.Categories)
	slices.Sort(sum.Hosts)
	for _, t := range m.MCP.Tools {
		for _, c := range t.PersonalData {
			if err := registry.ValidateDataCategory(c); err != nil {
				return DataUseSummary{}, fmt.Errorf("tool %q: %w", t.Name, err)
			}
			if c != registry.DataCategoryNone && !slices.Contains(m.DataUse.Categories, c) {
				return DataUseSummary{}, fmt.Errorf("tool %q declares personal data %q that dataUse.categories does not list", t.Name, c)
			}
			if c == registry.DataCategoryNone && len(t.PersonalData) > 1 {
				return DataUseSummary{}, fmt.Errorf("tool %q mixes \"none\" with other personal-data categories", t.Name)
			}
		}
		if len(t.PersonalData) > 0 {
			sum.ToolCategories[t.Name] = slices.Clone(t.PersonalData)
		}
	}
	return sum, nil
}
