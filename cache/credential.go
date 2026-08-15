package cache

import (
	"context"
	"fmt"
	"time"
)

type contextKey string

const (
	credentialKey         = contextKey("ms-cache-credential")
	credentialProviderKey = contextKey("ms-cache-credential-provider")
)

// Credential holds Redis connection details injected by the platform per invocation.
type Credential struct {
	Endpoint  string    `json:"endpoint"` // host:port
	Username  string    `json:"username"` // per-module ACL user (e.g., "mod_media")
	Token     string    `json:"token"`    // IAM auth token
	ExpiresAt time.Time `json:"expiresAt,omitzero"`
}

// CredentialProvider supplies renewable Redis credentials. Endpoint and
// Username must remain stable for the provider's lifetime.
type CredentialProvider interface {
	Credential(context.Context) (Credential, error)
}

// CredentialProviderKey may be implemented by a job-scoped provider to allow
// safe client reuse within that provider's lifetime. An unkeyed provider is
// never shared with another task attempt.
type CredentialProviderKey interface {
	CredentialProviderKey() string
}

// validate checks that all required fields are populated.
func (c Credential) validate() error {
	if c.Endpoint == "" || c.Username == "" || c.Token == "" {
		return fmt.Errorf("mirrorstack/cache: credential missing required fields (endpoint=%q username=%q)", c.Endpoint, c.Username)
	}
	return nil
}

// cacheKey returns the ClientCache key for this credential. Token is intentionally
// excluded so token rotation reuses the existing client rather than churning it.
func (c Credential) cacheKey() string {
	return c.Endpoint + "|" + c.Username
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

// WithCredentialProvider installs a renewable cache credential source.
func WithCredentialProvider(ctx context.Context, provider CredentialProvider) context.Context {
	return context.WithValue(ctx, credentialProviderKey, provider)
}

// CredentialProviderFrom reads the renewable cache credential source.
func CredentialProviderFrom(ctx context.Context) CredentialProvider {
	p, _ := ctx.Value(credentialProviderKey).(CredentialProvider)
	return p
}
