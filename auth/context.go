// Package auth provides authentication context and middleware for MirrorStack modules.
package auth

import "context"

const (
	RoleAdmin  = "admin"
	RoleMember = "member"
	RoleViewer = "viewer"
)

type contextKey string

const identityKey = contextKey("ms-identity")

// Identity holds the authenticated user's identity for the current request.
type Identity struct {
	UserID  string // Who
	AppID   string // Which app
	AppRole string // What access (admin, member, viewer)
}

// Set stores the identity in the context.
func Set(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey, &id)
}

// Get retrieves the identity from the context. Returns nil if not set.
//
//	a := auth.Get(r.Context())
//	a.UserID   // "u-123"
//	a.AppID    // "a-456"
//	a.AppRole  // "admin"
func Get(ctx context.Context) *Identity {
	id, _ := ctx.Value(identityKey).(*Identity)
	return id
}

// Roles is a convenience constructor for role lists.
func Roles(r ...string) []string { return r }
