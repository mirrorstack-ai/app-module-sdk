package module

// NavItem represents a navigation entry, quick action, or settings page
// contributed by a module to the platform UI.
type NavItem struct {
	Icon  string
	Label string
	Route string
}

// Contribution represents a UI component a module contributes to another
// module's view (e.g. a card on the user-detail page).
type Contribution struct {
	ID        string
	Title     string
	Component string
}

// ModuleManifest captures the full module declaration that the platform uses
// for routing, navigation, and inter-module contributions. It replaces
// per-module YAML files with type-safe Go values.
//
// Every module populates a ModuleManifest in its Manifest() method:
//
//	func (m *MyModule) Manifest() module.ModuleManifest {
//	    return module.ModuleManifest{
//	        ID:       "my-module",
//	        NameKey:  "module.mymodule.name",
//	        ...
//	    }
//	}
type ModuleManifest struct {
	ID             string
	NameKey        string
	DescriptionKey string
	Icon           string
	Category       string
	Version        string

	Dependencies         []string
	OptionalDependencies []string

	NavItems      []NavItem
	QuickActions  []NavItem
	SettingsPages []NavItem

	Contributions  map[string][]Contribution
	DefaultStrings map[string]string
}
