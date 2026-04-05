package auth

import (
	"net/http"
	"strings"
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
	registryMu  sync.RWMutex
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
	joinedRoles := strings.Join(roles, ", ")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role := AppRole(r.Context())
			if role == "" {
				httputil.JSON(w, http.StatusUnauthorized, httputil.ErrorResponse{Error: "authentication required"})
				return
			}
			if !roleSet[role] {
				httputil.JSON(w, http.StatusForbidden, httputil.ErrorResponse{Error: "permission " + name + " requires role: " + joinedRoles})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func registerPermission(p Permission) {
	registryMu.Lock()
	defer registryMu.Unlock()

	for _, existing := range permissions {
		if existing.Name == p.Name {
			return
		}
	}
	permissions = append(permissions, p)
}

// RegisteredPermissions returns a copy of all declared permissions.
func RegisteredPermissions() []Permission {
	registryMu.RLock()
	defer registryMu.RUnlock()

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
