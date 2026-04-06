package storage

import "context"

type contextKey string

const credentialKey = contextKey("ms-storage-credential")

// Credential holds S3 access details injected by the platform per invocation.
// STS temp credentials scoped to the app/module S3 prefix.
type Credential struct {
	Bucket          string `json:"bucket"`
	Region          string `json:"region"`
	Prefix          string `json:"prefix"`          // "apps/app_abc/mod_media/"
	CDNBase         string `json:"cdnBase"`          // "https://media.mirrorstack.ai"
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
	SessionToken    string `json:"sessionToken"`
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
