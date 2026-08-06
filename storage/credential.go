package storage

import (
	"context"
	"fmt"
)

type contextKey string

const credentialKey = contextKey("ms-storage-credential")

// Credential holds S3 access details injected by the platform per invocation.
// STS temp credentials scoped to the app/module S3 prefix.
type Credential struct {
	Bucket string `json:"bucket"`
	Region string `json:"region"`
	// Prefix is apps/<appUUID>/<moduleUUID>/ in production. Dev uses
	// apps/<appUUID>/<module-slug>/ because the SDK has no production module UUID.
	Prefix          string `json:"prefix"`
	CDNBase         string `json:"cdnBase"` // "https://media.mirrorstack.ai"
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
	SessionToken    string `json:"sessionToken"`
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
