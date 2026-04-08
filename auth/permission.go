package auth

import (
	"net/http"

	"github.com/mirrorstack-ai/app-module-sdk/internal/httputil"
)

// RequireRoles returns chi middleware that checks AppRole against the allowed
// roles. Pure middleware factory: no registry, no global state, safe to call
// from any goroutine.
//
// This function does NOT track the permission name in any module manifest —
// it only enforces the role check at request time. To both enforce AND surface
// the permission in the manifest payload, use Module.RequirePermission (or
// mirrorstack.RequirePermission, which dispatches to the default Module).
//
//	r.With(auth.RequireRoles("admin", "member", "viewer")).Get("/items", listItems)
func RequireRoles(roles ...string) func(http.Handler) http.Handler {
	roleSet := make(map[string]bool, len(roles))
	for _, r := range roles {
		roleSet[r] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			a := Get(r.Context())
			if a == nil || a.AppRole == "" {
				httputil.JSON(w, http.StatusUnauthorized, httputil.ErrorResponse{Error: "authentication required"})
				return
			}
			if !roleSet[a.AppRole] {
				httputil.JSON(w, http.StatusForbidden, httputil.ErrorResponse{Error: "forbidden"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
