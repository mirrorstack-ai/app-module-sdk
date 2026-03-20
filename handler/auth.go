package handler

import (
	"net/http"
	"strings"
)

// platformPermission represents platform user permission levels.
// Unexported to prevent external construction.
type platformPermission int

// Platform permission constants. Use these with RequirePlatformUser.
const (
	PlatformRead platformPermission = iota + 1
	PlatformWrite
	PlatformAdmin
)

// RequireInternal checks X-MS-Auth-Type is "internal".
// Use this to protect routes that only the platform should call (event handlers, schedules).
func RequireInternal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if !isContextExtracted(ctx) {
			Forbidden(w, "internal access required")
			return
		}
		if GetAuthType(ctx) != AuthTypeInternal {
			Forbidden(w, "internal access required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequirePlatformUser checks X-MS-Platform-User-ID is present and
// X-MS-Auth-Type is "platform".
//
// Permission-level enforcement is not yet implemented. Passing permission
// arguments will panic at startup to prevent false security guarantees.
func RequirePlatformUser(permissions ...platformPermission) func(http.Handler) http.Handler {
	if len(permissions) > 0 {
		panic("handler: permission-level enforcement is not yet implemented; do not pass permission arguments until org management ships")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			if !isContextExtracted(ctx) {
				Forbidden(w, "platform access required")
				return
			}

			userID := strings.TrimSpace(GetPlatformUserID(ctx))
			authType := GetAuthType(ctx)

			if userID == "" || authType != AuthTypePlatform {
				Forbidden(w, "platform access required")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
