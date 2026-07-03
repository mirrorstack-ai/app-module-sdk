// Package i18n is the module-side internationalization loader for the SDK.
//
// A module ships nested JSON catalogs (one file per locale, e.g.
// i18n/en-US.json, i18n/zh-TW.json) and registers them once at startup via
// RegisterMessages. Catalogs are flattened to dotted keys so a nested
// object like {"permissions":{"users.read":"View users"}} becomes the key
// "permissions.users.read".
//
// The catalogs feed Label values (Text / T). Labels are opaque: they carry
// either a literal string or a catalog key, and resolution to a per-locale
// map happens lazily — typically at manifest build time when a permission is
// declared (ms.RegisterPermission). This package never resolves at call time,
// so the order of RegisterMessages vs. building Labels does not matter as long
// as both run before the manifest is served.
//
// The registry is a process-global keyed by locale → flatKey → message. A
// MirrorStack module is a single binary serving a single module identity, so
// one global catalog per process is the right grain. Tests can call Reset to
// clear it.
package i18n

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
)

// DefaultLocale is the fallback locale. Literal Text() labels resolve under
// this locale, and catalog T() labels fall back to it (then to the raw key)
// when no loaded locale carries the requested message.
const DefaultLocale = "en-US"

// registry is the process-global message store: locale → flatKey → message.
// Guarded by mu. Populated by RegisterMessages; read by Label.Resolve.
var (
	mu       sync.RWMutex
	registry = map[string]map[string]string{}
)

// RegisterMessages loads every <dir>/<locale>.json file from fsys into the
// process-global catalog. The locale is the filename without its .json
// extension (e.g. "en-US", "zh-TW"). Each file must be a nested JSON object;
// it is flattened to dotted keys (a nested {"a":{"b":"x"}} becomes "a.b").
//
// Re-registering a locale merges into any existing entries for that locale
// (last write wins per key), so a module may load multiple catalog dirs.
// Returns an error if a file can't be read or isn't a JSON object; files that
// are not *.json (or are sub-directories) are skipped.
//
//	//go:embed i18n/*.json
//	var i18nFS embed.FS
//	ms.RegisterMessages(i18nFS, "i18n")
func RegisterMessages(fsys fs.FS, dir string) error {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return fmt.Errorf("i18n: read dir %q: %w", dir, err)
	}

	mu.Lock()
	defer mu.Unlock()

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		locale := strings.TrimSuffix(name, ".json")
		if locale == "" {
			continue
		}

		raw, err := fs.ReadFile(fsys, path.Join(dir, name))
		if err != nil {
			return fmt.Errorf("i18n: read file %q: %w", name, err)
		}

		var nested map[string]any
		if err := json.Unmarshal(raw, &nested); err != nil {
			return fmt.Errorf("i18n: %q is not a JSON object: %w", name, err)
		}

		flat := registry[locale]
		if flat == nil {
			flat = map[string]string{}
			registry[locale] = flat
		}
		flatten("", nested, flat)
	}
	return nil
}

// flatten walks a nested map, writing each leaf string under its dotted path
// into out. Non-string leaves (numbers, bools, arrays) are ignored — message
// catalogs are string-valued.
func flatten(prefix string, in map[string]any, out map[string]string) {
	for k, v := range in {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch t := v.(type) {
		case string:
			out[key] = t
		case map[string]any:
			flatten(key, t, out)
		}
	}
}

// Reset clears the process-global catalog. Test-only helper so each test
// starts from a known state.
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	registry = map[string]map[string]string{}
}

// Label is an opaque, deferred-resolution display string. Construct one with
// Text (a literal) or T (a catalog key). Resolution to a per-locale map happens
// later via Resolve, reading the catalog registered by RegisterMessages.
type Label struct {
	// literal is set for Text() labels: the string resolves to
	// {DefaultLocale: literal}. key is set for T() labels: it is looked up
	// in the registry per loaded locale. Exactly one of the two is meaningful;
	// isKey discriminates so an empty literal is still resolvable.
	literal string
	key     string
	isKey   bool
}

// Text returns a literal Label. It resolves to {DefaultLocale: s}, ignoring
// the catalog — use it for strings that are not translated (or whose
// translation lives elsewhere).
func Text(s string) Label { return Label{literal: s} }

// T returns a catalog-key Label. At Resolve time it is looked up per loaded
// locale (skipping locales that lack the key). If no loaded locale carries the
// key, it falls back to {DefaultLocale: key} — the raw key, NOT a humanized
// form — so a missing translation is unambiguous.
func T(key string) Label { return Label{key: key, isKey: true} }

// IsZero reports whether the Label was never set (the zero value). Callers use
// this to omit empty Labels from the manifest (e.g. an undeclared Description).
func (l Label) IsZero() bool {
	return !l.isKey && l.literal == ""
}

// Resolve returns the locale → message map for this Label, reading the catalog
// registered via RegisterMessages.
//
//   - A literal Text(s) resolves to {DefaultLocale: s}.
//   - A catalog T(key) resolves to {locale: messages[locale][key]} for every
//     loaded locale that has the key; locales missing the key are skipped. If
//     NO loaded locale has it, it falls back to {DefaultLocale: key}.
//
// The returned map is freshly allocated and owned by the caller.
func (l Label) Resolve() map[string]string {
	if !l.isKey {
		return map[string]string{DefaultLocale: l.literal}
	}

	mu.RLock()
	defer mu.RUnlock()

	out := map[string]string{}
	for locale, flat := range registry {
		if msg, ok := flat[l.key]; ok {
			out[locale] = msg
		}
	}
	if len(out) == 0 {
		// No translation anywhere — fall back to the raw key under the
		// default locale so the platform shows something unambiguous.
		return map[string]string{DefaultLocale: l.key}
	}
	return out
}

// Lookup returns the locale → message map for a catalog key. Unlike
// Label.Resolve it does NOT fall back to the raw key — an undeclared key yields
// an empty map, so callers (e.g. manifest defaults) can omit the field entirely
// and let a literal fallback win. The returned map is owned by the caller.
func Lookup(key string) map[string]string {
	mu.RLock()
	defer mu.RUnlock()
	out := map[string]string{}
	for locale, flat := range registry {
		if msg, ok := flat[key]; ok {
			out[locale] = msg
		}
	}
	return out
}

// LookupList resolves a catalog key whose per-locale value packs a LIST of
// items into one delimited string, returning locale → []item. Each locale's
// value is split on sep and every element is space-trimmed; empty elements are
// dropped, and a locale that yields no items is omitted. Like Lookup (and
// unlike Label.Resolve) it does NOT fall back to the raw key — an undeclared
// key yields an empty map, so a caller (e.g. manifest defaults) can omit the
// field and let a non-localized default win.
//
// It is the list counterpart of Lookup: Lookup(module.name)→NameLabels (one
// display string per locale), LookupList(module.tags, ",")→TagLabels (a tag
// LIST per locale). The delimited-string convention exists because message
// catalogs are string-valued — flatten() ignores JSON array leaves — so an
// author packs a tag list as e.g. en-US "Auth, Payments" / zh-TW "驗證, 付款".
// The returned map is owned by the caller.
func LookupList(key, sep string) map[string][]string {
	mu.RLock()
	defer mu.RUnlock()
	out := map[string][]string{}
	for locale, flat := range registry {
		raw, ok := flat[key]
		if !ok {
			continue
		}
		var list []string
		for _, part := range strings.Split(raw, sep) {
			if part = strings.TrimSpace(part); part != "" {
				list = append(list, part)
			}
		}
		if len(list) > 0 {
			out[locale] = list
		}
	}
	return out
}

// Locales returns the sorted list of locales currently loaded. Test/debug aid.
func Locales() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(registry))
	for locale := range registry {
		out = append(out, locale)
	}
	sort.Strings(out)
	return out
}
