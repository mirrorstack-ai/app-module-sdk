// Package auth provides authentication context and middleware for MirrorStack modules.
package auth

import "context"

// Roles
const (
	RoleAdmin  = "admin"
	RoleMember = "member"
	RoleViewer = "viewer"
)

type contextKey string

const (
	userIDKey  = contextKey("ms-user-id")
	appIDKey   = contextKey("ms-app-id")
	appRoleKey = contextKey("ms-app-role")
)

// WithUserID returns a context with the user ID set.
func WithUserID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, userIDKey, id)
}

// UserID reads the user ID from the context.
func UserID(ctx context.Context) string {
	s, _ := ctx.Value(userIDKey).(string)
	return s
}

// WithAppID returns a context with the app ID set.
func WithAppID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, appIDKey, id)
}

// AppID reads the app ID from the context.
func AppID(ctx context.Context) string {
	s, _ := ctx.Value(appIDKey).(string)
	return s
}

// WithAppRole returns a context with the app role set.
func WithAppRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, appRoleKey, role)
}

// AppRole reads the app role from the context.
func AppRole(ctx context.Context) string {
	s, _ := ctx.Value(appRoleKey).(string)
	return s
}

// Roles is a convenience constructor for role lists.
func Roles(r ...string) []string { return r }
