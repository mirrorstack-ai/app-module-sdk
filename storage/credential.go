package storage

import (
	"context"
	"fmt"
	"time"
)

type contextKey string

const (
	credentialKey         = contextKey("ms-storage-credential")
	credentialProviderKey = contextKey("ms-storage-credential-provider")
)

// Credential holds S3 access details injected by the platform per invocation.
// STS temp credentials scoped to the app/module S3 prefix.
type Credential struct {
	Bucket string `json:"bucket"`
	Region string `json:"region"`
	// Prefix is apps/<appUUID>/<moduleUUID>/ in production. Dev uses the
	// platform-minted Config.ID in the module segment because no installed
	// module UUID exists in a local tunnel session.
	Prefix          string `json:"prefix"`
	CDNBase         string `json:"cdnBase"` // "https://media.mirrorstack.ai"
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
	SessionToken    string `json:"sessionToken"`
	// ExpiresAt is the STS credential expiry. Older platform envelopes may omit
	// it; clients preserve that compatibility while newer envelopes use it to
	// keep presigned URLs within the credential lifetime.
	ExpiresAt time.Time `json:"expiresAt,omitzero"`
	// MaxUploadBytes is the per-module upload ceiling: the largest size a
	// presigned PUT minted from this credential may declare via
	// storage.WithMaxBytes. It travels in the vend (not env) because env is a
	// provision-time snapshot of a module Lambda and would need a redeploy to
	// change, while this is recorded at publish from ms.RequireStorage(ms.MaxUpload(n))
	// and can change whenever the module republishes.
	//
	// Like ExpiresAt, older platform envelopes may omit it — it is new in this
	// release. A zero value means the SDK was not told a ceiling: PresignPut
	// still signs whatever WithMaxBytes declares, it just cannot bound it
	// against a limit it was never given.
	MaxUploadBytes int64 `json:"maxUploadBytes,omitempty"`
}

// CredentialProvider supplies renewable scoped storage credentials. Bucket,
// Region, Prefix and CDNBase must remain stable for the provider's lifetime.
type CredentialProvider interface {
	Credential(context.Context) (Credential, error)
}

// validate checks that all required fields are populated. Prefix is required
// and must end in "/":
// without it, tenant isolation silently collapses to the bucket root because
// every key is concatenated as `prefix + key` with an empty prefix. The trailing
// slash prevents a policy for one prefix from matching a sibling whose ID merely
// starts with the same bytes. Storage
// credentials are not pool-cached (STS rotation makes caching by AccessKeyID
// unsafe), so there is no cacheKey method. SessionToken is optional —
// long-lived IAM keys are valid in dev/test setups.
func (c Credential) validate() error {
	if c.Bucket == "" || c.Region == "" || c.Prefix == "" || c.CDNBase == "" || c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return fmt.Errorf("mirrorstack/storage: credential missing required fields (bucket=%q region=%q prefix=%q cdnBase=%q)", c.Bucket, c.Region, c.Prefix, c.CDNBase)
	}
	if c.Prefix[len(c.Prefix)-1] != '/' {
		return fmt.Errorf("mirrorstack/storage: credential prefix must end with a trailing slash to preserve the storage policy boundary (prefix=%q)", c.Prefix)
	}
	return nil
}

// WithCredential returns a context with the storage credential set.
func WithCredential(ctx context.Context, cred Credential) context.Context {
	return context.WithValue(ctx, credentialKey, &cred)
}

// CredentialFrom reads the storage credential from the context.
func CredentialFrom(ctx context.Context) *Credential {
	c, _ := ctx.Value(credentialKey).(*Credential)
	return c
}

// WithCredentialProvider installs a renewable storage credential source.
func WithCredentialProvider(ctx context.Context, provider CredentialProvider) context.Context {
	return context.WithValue(ctx, credentialProviderKey, provider)
}

// CredentialProviderFrom reads the renewable storage credential source.
func CredentialProviderFrom(ctx context.Context) CredentialProvider {
	p, _ := ctx.Value(credentialProviderKey).(CredentialProvider)
	return p
}

// RequireSettings accumulates ms.RequireStorage options. It is exported (not
// the option funcs' private closure state) so internal/core can resolve
// options into a plain value without this package depending on the registry
// package, or the registry package depending on this one.
type RequireSettings struct {
	// MaxUploadBytes is the module's declared upload ceiling, recorded in the
	// manifest at publish and vended back on Credential.MaxUploadBytes. Zero
	// means no declaration was made — absent declaration = platform default.
	MaxUploadBytes int64
}

// RequireOption configures ms.RequireStorage.
type RequireOption func(*RequireSettings)

// MaxUpload declares the largest file this module will ever ask a presigned
// PUT to accept. It is recorded in the manifest at publish by api-platform and
// vended back as Credential.MaxUploadBytes, which PresignPut's WithMaxBytes
// checks declared sizes against. Absent declaration = platform default.
//
// Panics if n <= 0, the same way the SDK's other declare-time options
// (ms.Meter's kind/kind-conflict rules) fail fast at startup rather than at
// first use: a module ships with its own RequireStorage call, so a mistake
// here is a bug in the module's own startup code, not bad input to validate.
func MaxUpload(n int64) RequireOption {
	if n <= 0 {
		panic(fmt.Sprintf("mirrorstack/storage: MaxUpload requires a positive byte count, got %d", n))
	}
	return func(s *RequireSettings) { s.MaxUploadBytes = n }
}
