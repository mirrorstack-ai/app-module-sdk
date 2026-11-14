package registry

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Data categories a module may declare it handles. A closed vocabulary on
// purpose: a free-text category cannot be compared, filtered or shown on a
// consent page, and a typo would read as a different, undeclared category.
const (
	DataCategoryNone      = "none"      // handles no personal data at all
	DataCategoryIdentity  = "identity"  // name, user id, handle, avatar
	DataCategoryContact   = "contact"   // email, phone, postal address
	DataCategoryContent   = "content"   // text, files or media a person wrote or uploaded
	DataCategoryUsage     = "usage"     // activity, clicks, history, telemetry
	DataCategoryFinancial = "financial" // payment, invoice or order records
	DataCategoryLocation  = "location"  // coordinates, IP-derived place
	DataCategorySensitive = "sensitive" // health, biometric, children's data, anything regulated
)

// DataCategories is the closed vocabulary, sorted, for error messages.
var DataCategories = []string{
	DataCategoryContact, DataCategoryContent, DataCategoryFinancial, DataCategoryIdentity,
	DataCategoryLocation, DataCategoryNone, DataCategorySensitive, DataCategoryUsage,
}

// DataUse is the module's declaration of what personal data it touches, how
// long it keeps it, and which outside hosts it sends data to. It rides the
// manifest as "dataUse" so the platform can show it to the person approving
// the module and refuse a module that never made one.
type DataUse struct {
	// Categories lists every kind of personal data the module handles. Exactly
	// ["none"] declares that it handles none.
	Categories []string `json:"categories"`
	// Retention is how long the module keeps that data: "none" (not stored),
	// "until-deleted" (kept until the person or app owner deletes it), or a
	// period such as "30d", "6m" or "1y".
	Retention string `json:"retention"`
	// Hosts lists every outside host the module sends data to (bare lower-case
	// host names, no scheme, port, path or wildcard). Empty means it sends data
	// nowhere outside the platform. The platform allows an agent-built module
	// outbound access to these hosts and no others.
	Hosts []string `json:"hosts"`
}

var (
	retentionPeriod = regexp.MustCompile(`^[1-9][0-9]{0,3}[dmy]$`)
	hostName        = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
)

// ValidateDataCategory reports whether c is in the closed vocabulary.
func ValidateDataCategory(c string) error {
	if !slices.Contains(DataCategories, c) {
		return fmt.Errorf("unknown data category %q (allowed: %s)", c, strings.Join(DataCategories, ", "))
	}
	return nil
}

// Validate reports the first problem with the declaration, or nil. It is the
// one rule set the SDK (at Init) and the platform gate (on the manifest) share.
func (d DataUse) Validate() error {
	if len(d.Categories) == 0 {
		return fmt.Errorf("dataUse.categories is empty (declare the categories, or [\"none\"])")
	}
	seen := map[string]bool{}
	for _, c := range d.Categories {
		if err := ValidateDataCategory(c); err != nil {
			return fmt.Errorf("dataUse.categories: %w", err)
		}
		if seen[c] {
			return fmt.Errorf("dataUse.categories lists %q twice", c)
		}
		seen[c] = true
	}
	if seen[DataCategoryNone] && len(d.Categories) > 1 {
		return fmt.Errorf("dataUse.categories mixes \"none\" with other categories")
	}
	if d.Retention != "none" && d.Retention != "until-deleted" && !retentionPeriod.MatchString(d.Retention) {
		return fmt.Errorf("dataUse.retention %q is not none, until-deleted or a period like 30d, 6m, 1y", d.Retention)
	}
	for _, h := range d.Hosts {
		if !hostName.MatchString(h) {
			return fmt.Errorf("dataUse.hosts: %q is not a bare lower-case host name (no scheme, port, path or wildcard)", h)
		}
	}
	return nil
}

// normalized returns a copy with sorted categories and hosts and a non-nil
// Hosts slice, so the manifest bytes (and so the manifest hash) do not depend
// on declaration order and the wire shape always carries "hosts": [].
func (d DataUse) normalized() DataUse {
	out := DataUse{Retention: d.Retention}
	out.Categories = slices.Clone(d.Categories)
	sort.Strings(out.Categories)
	out.Hosts = slices.Clone(d.Hosts)
	if out.Hosts == nil {
		out.Hosts = []string{}
	}
	sort.Strings(out.Hosts)
	return out
}

// SetDataUse records the module's declaration. The caller validates first.
func (r *Registry) SetDataUse(d DataUse) {
	n := d.normalized()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dataUse = &n
}

// DataUse returns a copy of the declaration, or nil when the module made none.
func (r *Registry) DataUse() *DataUse {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.dataUse == nil {
		return nil
	}
	n := r.dataUse.normalized()
	return &n
}
