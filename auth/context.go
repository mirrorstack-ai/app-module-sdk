// Package auth provides authentication context and middleware for MirrorStack modules.
//
// Module code reads the request identity from the CONTEXT — ms.UserID /
// ms.AppID / ms.AppRole (or auth.Get) — never from the X-MS-* Header*
// constants, which are the internal platform-to-SDK wire; see the Header*
// docs for why header reads silently break deployed.
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

const payloadTrustKey = contextKey("ms-payload-trust")

const proxyTrustKey = contextKey("ms-proxy-trust")

// withProxyTrust marks the HTTP request after RequireProxy validates the
// server-only platform token. PlatformAuth uses this private mark to know that
// it may capture the dispatch-issued actor delegation header. Public routes do
// not run PlatformAuth, so they never gain a delegation merely by being
// proxied.
func withProxyTrust(ctx context.Context) context.Context {
	return context.WithValue(ctx, proxyTrustKey, true)
}

func proxyTrusted(ctx context.Context) bool {
	trusted, _ := ctx.Value(proxyTrustKey).(bool)
	return trusted
}

// WithPayloadTrust marks ctx as carrying identity injected from the typed
// Lambda payload — set ONLY by runtime.NewLambdaHandler (the real Lambda
// invoke path, or the dev lambda-invoke shim after its envelope-secret gate).
// RequireProxy gives a marked request the same pass-through it gives Lambda
// mode: the payload IS the trust boundary, and the envelope never carries the
// per-session X-MS-Platform-Token. The mark lives in context, so inbound
// request data can never set it.
func WithPayloadTrust(ctx context.Context) context.Context {
	return context.WithValue(ctx, payloadTrustKey, true)
}

// PayloadTrusted reports whether WithPayloadTrust marked this context.
func PayloadTrusted(ctx context.Context) bool {
	trusted, _ := ctx.Value(payloadTrustKey).(bool)
	return trusted
}

// Roles is a convenience constructor for role lists.
func Roles(r ...string) []string { return r }
