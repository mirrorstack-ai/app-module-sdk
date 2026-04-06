package cache

import "context"

type contextKey string

const credentialKey = contextKey("ms-cache-credential")

// Credential holds Redis connection details injected by the platform per invocation.
type Credential struct {
	Endpoint string `json:"endpoint"` // host:port
	Username string `json:"username"` // per-module ACL user (e.g., "mod_media")
	Token    string `json:"token"`    // IAM auth token
}

// WithCredential returns a context with the cache credential set.
func WithCredential(ctx context.Context, cred Credential) context.Context {
	return context.WithValue(ctx, credentialKey, &cred)
}

// CredentialFrom reads the cache credential from the context.
func CredentialFrom(ctx context.Context) *Credential {
	c, _ := ctx.Value(credentialKey).(*Credential)
	return c
}
