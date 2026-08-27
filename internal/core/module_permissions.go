// Package mirrorstack is the Go SDK for building modules on MirrorStack.
//
// Use Init() + Start() for the convenience API, or New() for testing and advanced use.
package core

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/i18n"
	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
	"github.com/mirrorstack-ai/app-module-sdk/internal/runtime"
	"github.com/mirrorstack-ai/app-module-sdk/roles"
)

// This file holds permission declaration, qualification and enforcement, plus the i18n label seams they resolve through.
//
// Split out of a single 1069-line module.go — the SDK's own version of the
// catch-all this codebase tells module authors not to write.

// directly.
type Label = i18n.Label

// Text returns a literal Label resolving to {i18n.DefaultLocale: s}.
func Text(s string) Label { return i18n.Text(s) }

// T returns a catalog-key Label resolved per loaded locale at manifest-build
// time. See i18n.T.
func T(key string) Label { return i18n.T(key) }

// RegisterMessages loads the module's i18n catalogs (one <locale>.json per
// file under dir) into the SDK's message registry. See i18n.RegisterMessages.
func RegisterMessages(fsys fs.FS, dir string) error { return i18n.RegisterMessages(fsys, dir) }

// PermissionOpts configures a permission declared via RegisterPermission.
//
// DefaultRole is the role the permission lands on (commonly roles.Viewer();
// roles.Admin() keeps it admin-only). Admin is ALWAYS implicit — it is never
// stored in the role set. CustomRoles are extra role keys beyond the default.
//
// Label and Description are optional display strings, built from ms.Text /
// ms.T. They are resolved against the module's i18n catalogs at registration
// and folded into the manifest (per-locale), so the platform's roles UI can
// show a localized human label instead of the raw permission key. A zero
// Label is omitted entirely.
type PermissionOpts struct {
	DefaultRole roles.Role
	CustomRoles []string
	Label       Label
	Description Label
}

// RegisterPermission declares a module permission ONCE, recording its default
// role, custom roles, and resolved i18n Labels/Descriptions into this Module's
// registry so they appear in the manifest. Call it at startup (typically in a
// declare/ package) BEFORE the routes that gate on it. First-wins by name:
// re-declaring the same permission is a no-op.
//
// Permission names are auto-namespaced with the module slug so two modules can
// each declare a "users.read" without colliding in the platform's catalog.
// Developers pass the SHORT name; the manifest stores "<slug>.<name>". If the
// caller already prefixed the name with the slug, the prefix is left alone.
//
//	import "github.com/mirrorstack-ai/app-module-sdk/roles"
//	ms.RegisterPermission("users.read", ms.PermissionOpts{
//	    DefaultRole: roles.Viewer(),
//	    Label:       ms.T("permissions.users.read"),
//	})
func (m *Module) RegisterPermission(name string, opts PermissionOpts) {
	// Manifest records the default role + any custom roles; admin is implicit.
	declared := append([]string{opts.DefaultRole.Key}, opts.CustomRoles...)
	perm := registry.Permission{
		Name:        qualifyPermissionName(m.config.Slug, name),
		Roles:       declared,
		DefaultRole: opts.DefaultRole.Key,
	}
	if !opts.Label.IsZero() {
		perm.Labels = opts.Label.Resolve()
	}
	if !opts.Description.IsZero() {
		perm.Descriptions = opts.Description.Resolve()
	}
	m.registry.AddPermissionWithMeta(perm)
}

// RequirePermission returns chi middleware that gates a route on a permission
// declared via RegisterPermission, looked up BY NAME. It reads the registered
// permission's role set and enforces it (admin always passes). Call this at
// route registration time (alongside m.Platform/Public/Internal), NOT from
// inside a request handler.
//
//	import "github.com/mirrorstack-ai/app-module-sdk/roles"
//	// in declare/: ms.RegisterPermission("users.view", ms.PermissionOpts{DefaultRole: roles.Viewer()})
//	// in routes:
//	r.With(mod.RequirePermission("users.view")).Get("/items", listItems)
//
// The name is slug-qualified the same way RegisterPermission qualifies it, so
// the short name used here matches the short name used there.
//
// Safe-by-default: if the name was never RegisterPermission'd, it is registered
// lazily as ADMIN-ONLY (an isDev warning is logged) so a forgotten declaration
// locks the route down rather than opening it.
func (m *Module) RequirePermission(name string) func(http.Handler) http.Handler {
	_, declared := m.ensurePermissionDeclared("RequirePermission", name)
	// Runtime: admin always passes, plus the declared roles.
	return auth.RequireRoles(append([]string{roles.Admin().Key}, declared...)...)
}

// ensurePermissionDeclared slug-qualifies name and returns the qualified name
// plus the declared role set. Lazy admin-only registration: a surface gated on
// an undeclared permission must NOT silently open, so a missing
// RegisterPermission registers the name admin-only (a dev warning names the
// calling surface so the author notices). Shared by RequirePermission and
// MCPTool's ToolPermission option.
func (m *Module) ensurePermissionDeclared(caller, name string) (string, []string) {
	qualified := qualifyPermissionName(m.config.Slug, name)
	declared, ok := m.registry.PermissionRoles(qualified)
	if !ok {
		if !runtime.IsLambda() {
			m.logger.Printf("%s(%q): no RegisterPermission found — defaulting to admin-only. Declare it via ms.RegisterPermission.", caller, name)
		}
		m.registry.AddPermissionWithMeta(registry.Permission{
			Name:        qualified,
			Roles:       []string{roles.Admin().Key},
			DefaultRole: roles.Admin().Key,
		})
		declared = []string{roles.Admin().Key}
	}
	return qualified, declared
}

// qualifyPermissionName prepends "<slug>." to name. No-op if name is
// already slug-prefixed, or if the module has no slug (avoids a
// leading "." for test fixtures and internal-only modules).
func qualifyPermissionName(slug, name string) string {
	if slug == "" {
		return name
	}
	prefix := slug + "."
	if strings.HasPrefix(name, prefix) {
		return name
	}
	return prefix + name
}

// RegisterPermission declares a permission on the default Module created by
// Init(). Calling this before Init() panics — match Platform/Public/Internal.
func RegisterPermission(name string, opts PermissionOpts) {
	mustDefault("RegisterPermission").RegisterPermission(name, opts)
}

// RequirePermission is the convenience wrapper that dispatches to the default
// Module created by Init(). Calling this before Init() panics — match the
// behavior of Platform/Public/Internal.
//
//	ms.Init(...)
//	ms.RegisterPermission("media.view", ms.PermissionOpts{DefaultRole: roles.Viewer()})
//	ms.Platform(func(r chi.Router) {
//	    r.With(ms.RequirePermission("media.view")).Get(...)
//	})
func RequirePermission(name string) func(http.Handler) http.Handler {
	return mustDefault("RequirePermission").RequirePermission(name)
}

// Start auto-detects the runtime mode and starts serving:
//
//   - Lambda (AWS_LAMBDA_FUNCTION_NAME set): wraps the router for Lambda invoke
//   - One-shot task (MS_TASK_ONE_SHOT=1): claims and runs one broker attempt
//   - Otherwise: HTTP server on :PORT (default 8080) for local development
//
// checkUIServable rejects the one combination that is always a mistake:
// RegisterUI declared a surface, but Config.WebDir is empty so ms.Start never
// mounts a directory behind GET /__mirrorstack/web/*.
//
// Nothing else catches it. The manifest still advertises the page, the module
// still builds, its bundle still builds, and CI still passes — the platform
// dynamically imports /__mirrorstack/web/index.js, gets a 404, and the admin
// sees "Couldn't load module bundle". The failure is entirely in the wiring
// between a declaration and the runtime meant to serve it, so it only ever
// shows up in a browser.
//
// A module with no UI is fine, and a module with WebDir and no UI is fine too
// (it may serve assets a contribution renders). Only declared-but-unservable
// is contradictory, and it is worth failing the process for: the module is
