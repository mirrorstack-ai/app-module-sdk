package core

import "github.com/mirrorstack-ai/app-module-sdk/internal/registry"

// Public type aliases for the UI manifest. The data shapes live in the
// registry package (alongside the storage methods); core re-exports them
// so module authors construct ms.ModuleUI{...} without importing internal/.
type (
	ModuleUI    = registry.ModuleUI
	UIComponent = registry.UIComponent
	UIProp      = registry.UIProp
	UIPage      = registry.UIPage
)

// RegisterUI declares the module's UI surface — agent-visible Components
// plus DefaultPages mounted under /apps/<app-slug>/<module-slug>. See the
// ModuleUI doc comment for the shape. Validates the input and panics on
// programmer errors (duplicate names, invalid slug, unknown prop type).
// Last-write-wins; a second call replaces the prior manifest.
//
//	ms.RegisterUI(ms.ModuleUI{
//	    Components: []ms.UIComponent{
//	        {Name: "SettingsForm", Export: "SettingsForm", Props: []ms.UIProp{
//	            {Key: "appId", Type: "text", Required: true},
//	        }},
//	    },
//	    DefaultPages: []ms.UIPage{
//	        {Slug: "", Title: "OAuth Settings", Export: "SettingsPage"},
//	    },
//	})
func (m *Module) RegisterUI(ui ModuleUI) {
	m.registry.SetUI(ui)
}

// RegisterUI declares the default module's UI surface. Panics if Init
// has not been called. See Module.RegisterUI.
func RegisterUI(ui ModuleUI) { mustDefault("RegisterUI").RegisterUI(ui) }
