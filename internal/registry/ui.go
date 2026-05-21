package registry

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// validPropTypes locks the v0 prop type vocabulary. Adding a new type means
// updating the renderer in web-applications + the agent's schema awareness;
// rejecting unknown types here surfaces that as a programmer error at
// module init instead of as a silent render failure later. The slice is
// also used to compose the panic message so the "allowed" list stays in
// lockstep with the actual check.
var validPropTypes = []string{"text", "secret", "textarea", "bool", "number", "text-list"}

// ModuleUI is the module's declared UI surface. Two parts:
//
//   - Components: the module's agent-visible React vocabulary. Each entry
//     names a React export the module's bundle ships, plus a prop schema
//     so the agent envelope layer (dynamic-ui v1) can compose them.
//   - DefaultPages: the module's own React pages, mounted by the platform
//     under /apps/<app-slug>/<module-slug>/<page-slug>. Each page is a
//     full React export — the module has total layout freedom.
//
// A module without a UI surface omits the ms.RegisterUI call entirely;
// Registry.UI() returns nil and the manifest omits the field.
type ModuleUI struct {
	Components   []UIComponent `json:"components"`
	DefaultPages []UIPage      `json:"defaultPages"`
}

// UIComponent declares one agent-visible React component shipped by the
// module's web bundle. Name is how the component is referenced from agent
// envelopes (namespaced "<module-slug>/<Name>" at the platform layer);
// Export is the corresponding named export in web/index.tsx. Props is the
// schema the agent uses to know what to pass in.
type UIComponent struct {
	Name   string   `json:"name"`
	Export string   `json:"export"`
	Props  []UIProp `json:"props,omitempty"`
}

// UIProp is one prop declared on a UIComponent. Type is one of the v0 set:
// "text", "secret", "textarea", "bool", "number", "text-list". Required
// defaults to false; Default carries the literal default value (any JSON);
// Hint is freeform help text shown to the agent / in dev tooling.
type UIProp struct {
	Key      string `json:"key"`
	Type     string `json:"type"`
	Required bool   `json:"required,omitempty"`
	Default  any    `json:"default,omitempty"`
	Hint     string `json:"hint,omitempty"`
}

// UIPage is one entry in DefaultPages — the module's own React page mounted
// at /apps/<app-slug>/<module-slug>/<Slug>. Empty Slug is the index page.
// Export names the bundle's React export to mount.
type UIPage struct {
	Slug        string `json:"slug"`
	Icon        string `json:"icon,omitempty"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Export      string `json:"export"`
}

// pageSlugRe is the slug rule from the v0 plan: lowercase letters, digits,
// and hyphens, 1–32 chars, must start and end with a letter or digit (no
// leading/trailing hyphens). Empty slug (the index page) is checked
// separately and skips this pattern.
var pageSlugRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

// reservedSlugPrefixes are platform-reserved namespaces under the module
// route. The platform may mount its own surfaces under these prefixes in
// the future (e.g. /__ms/* for platform-rendered fallbacks); reserving
// them at the SDK avoids a future collision.
var reservedSlugPrefixes = []string{"_", "__ms"}

// SetUI stores the module's declared UI manifest. Validates the input and
// panics on programmer errors (duplicate names, invalid slug, unknown prop
// type). Last-write-wins; a second call replaces the first. The stored
// value is a deep copy so callers can mutate their input afterwards
// without aliasing into the registry.
func (r *Registry) SetUI(ui ModuleUI) {
	validateUI(ui)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ui = cloneUI(ui)
}

// UI returns a deep copy of the stored manifest, or nil if SetUI was never
// called. Nil rather than an empty zero-value so the manifest endpoint can
// distinguish "no UI" (omit the field) from "UI with empty lists".
func (r *Registry) UI() *ModuleUI {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.ui == nil {
		return nil
	}
	return cloneUI(*r.ui)
}

func validateUI(ui ModuleUI) {
	seenComp := make(map[string]struct{}, len(ui.Components))
	for i, c := range ui.Components {
		if c.Name == "" {
			panic(fmt.Sprintf("mirrorstack: RegisterUI: Components[%d].Name is empty", i))
		}
		if c.Export == "" {
			panic(fmt.Sprintf("mirrorstack: RegisterUI: Components[%d] (%q) Export is empty", i, c.Name))
		}
		if _, dup := seenComp[c.Name]; dup {
			panic(fmt.Sprintf("mirrorstack: RegisterUI: duplicate Component name %q", c.Name))
		}
		seenComp[c.Name] = struct{}{}
		validateProps(c.Name, c.Props)
	}

	seenSlug := make(map[string]struct{}, len(ui.DefaultPages))
	for i, p := range ui.DefaultPages {
		if p.Title == "" {
			panic(fmt.Sprintf("mirrorstack: RegisterUI: DefaultPages[%d] Title is empty", i))
		}
		if p.Export == "" {
			panic(fmt.Sprintf("mirrorstack: RegisterUI: DefaultPages[%d] (%q) Export is empty", i, p.Title))
		}
		validatePageSlug(p.Slug, i)
		if _, dup := seenSlug[p.Slug]; dup {
			panic(fmt.Sprintf("mirrorstack: RegisterUI: duplicate DefaultPages slug %q", p.Slug))
		}
		seenSlug[p.Slug] = struct{}{}
	}
}

func validateProps(componentName string, props []UIProp) {
	seen := make(map[string]struct{}, len(props))
	for i, p := range props {
		if p.Key == "" {
			panic(fmt.Sprintf("mirrorstack: RegisterUI: %s.Props[%d].Key is empty", componentName, i))
		}
		if _, dup := seen[p.Key]; dup {
			panic(fmt.Sprintf("mirrorstack: RegisterUI: %s has duplicate Prop key %q", componentName, p.Key))
		}
		seen[p.Key] = struct{}{}
		if !slices.Contains(validPropTypes, p.Type) {
			panic(fmt.Sprintf("mirrorstack: RegisterUI: %s.Props[%d] (%q) invalid type %q (allowed: %s)", componentName, i, p.Key, p.Type, strings.Join(validPropTypes, ", ")))
		}
	}
}

func validatePageSlug(slug string, index int) {
	if slug == "" {
		return
	}
	for _, prefix := range reservedSlugPrefixes {
		if slug == prefix || strings.HasPrefix(slug, prefix+"/") || strings.HasPrefix(slug, prefix+"-") {
			panic(fmt.Sprintf("mirrorstack: RegisterUI: DefaultPages[%d] slug %q uses reserved prefix %q", index, slug, prefix))
		}
	}
	if !pageSlugRe.MatchString(slug) {
		panic(fmt.Sprintf("mirrorstack: RegisterUI: DefaultPages[%d] slug %q is invalid (lowercase letters, digits, hyphens; 1-32 chars; must start with a letter or digit)", index, slug))
	}
}

func cloneUI(ui ModuleUI) *ModuleUI {
	out := &ModuleUI{
		Components:   make([]UIComponent, len(ui.Components)),
		DefaultPages: slices.Clone(ui.DefaultPages),
	}
	for i, c := range ui.Components {
		out.Components[i] = UIComponent{
			Name:   c.Name,
			Export: c.Export,
			Props:  slices.Clone(c.Props),
		}
	}
	return out
}
