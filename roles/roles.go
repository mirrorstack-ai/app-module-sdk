// Package roles defines typed role values passed to ms.RequirePermission.
//
// Typed values prevent typos ("Admian", "VIEWER"), enable IDE autocomplete,
// and reserve space for future i18n / hierarchy metadata without breaking
// the call-site API. The canonical roles are Admin and Viewer; Custom
// accepts any string for module-specific roles.
//
//	import p "github.com/mirrorstack-ai/app-module-sdk/roles"
//
//	ms.RequirePermission("media.view", p.Admin(), p.Viewer())
//	ms.RequirePermission("media.moderate", p.Custom("moderator"))
package roles

// Role is a typed wrapper around a role key. Equality comparisons use Key.
type Role struct {
	Key string
}

// Admin is the canonical administrator role.
const adminKey = "admin"

// Viewer is the canonical read-only role.
const viewerKey = "viewer"

// Admin returns the canonical administrator role.
func Admin() Role { return Role{Key: adminKey} }

// Viewer returns the canonical read-only role.
func Viewer() Role { return Role{Key: viewerKey} }

// Custom returns a Role with the given arbitrary key. Use when Admin/Viewer
// don't fit — typically for module-specific roles like "moderator" or
// "billing_ops". Panics on empty key.
func Custom(key string) Role {
	if key == "" {
		panic("mirrorstack/roles: Custom requires a non-empty key")
	}
	return Role{Key: key}
}

// Keys returns the bare role keys from a slice of Role, preserving order.
// Used internally by ms.RequirePermission to interoperate with auth's
// string-based middleware.
func Keys(rs []Role) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Key
	}
	return out
}
