package auth

import (
	"net/http"
	"sync"

	"github.com/mirrorstack-ai/app-module-sdk/internal/httputil"
)

// Permission represents a declared module permission.
type Permission struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Roles       []string `json:"roles"`
}

var (
	registryMu  sync.Mutex
	permissions []Permission
)

// RequirePermission returns chi middleware that checks AppRole against allowed roles.
// Registers the permission for manifest generation.
//
//	r.With(auth.RequirePermission("media.view", "admin", "member", "viewer")).Get("/items", listItems)
func RequirePermission(name string, roles ...string) func(http.Handler) http.Handler {
	registerPermission(Permission{Name: name, Roles: roles})

	roleSet := make(map[string]bool, len(roles))
	for _, r := range roles {
		roleSet[r] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role := AppRole(r.Context())
			if role == "" {
				httputil.JSON(w, http.StatusUnauthorized, errorResponse{Error: "authentication required"})
				return
			}
			if !roleSet[role] {
				httputil.JSON(w, http.StatusForbidden, errorResponse{Error: "permission " + name + " requires role: " + joinRoles(roles)})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func registerPermission(p Permission) {
	registryMu.Lock()
	defer registryMu.Unlock()

	// Skip duplicates
	for _, existing := range permissions {
		if existing.Name == p.Name {
			return
		}
	}
	permissions = append(permissions, p)
}

// RegisteredPermissions returns a copy of all declared permissions.
// Used by the manifest endpoint.
func RegisteredPermissions() []Permission {
	registryMu.Lock()
	defer registryMu.Unlock()

	result := make([]Permission, len(permissions))
	copy(result, permissions)
	return result
}

// ResetPermissions clears the registry. For testing only.
func ResetPermissions() {
	registryMu.Lock()
	defer registryMu.Unlock()
	permissions = nil
}

func joinRoles(roles []string) string {
	s := ""
	for i, r := range roles {
		if i > 0 {
			s += ", "
		}
		s += r
	}
	return s
}
