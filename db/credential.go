package db

import (
	"context"
	"fmt"
)

type contextKey string

const (
	schemaKey           = contextKey("ms-app-schema")
	credentialKey       = contextKey("ms-db-credential")
	moduleCredentialKey = contextKey("ms-db-module-credential")
)

// Credential holds per-invocation database credentials injected by the
// platform. The same struct serves both per-app and per-module credentials,
// distinguished by the context key they live under (credentialKey vs
// moduleCredentialKey). Different DB usernames separate pool cache keys
// naturally — no flag on the struct is needed.
type Credential struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	Username string `json:"username"`
	Token    string `json:"token"`
}

// validate checks that all required fields are populated.
func (c Credential) validate() error {
	if c.Host == "" || c.Port == 0 || c.Database == "" || c.Username == "" || c.Token == "" {
		return fmt.Errorf("mirrorstack/db: credential missing required fields (host=%q port=%d db=%q user=%q)", c.Host, c.Port, c.Database, c.Username)
	}
	return nil
}

// cacheKey returns the PoolCache key for this credential. Token is intentionally
// excluded so token rotation reuses the existing pool rather than churning it.
func (c Credential) cacheKey() string {
	return fmt.Sprintf("%s:%d/%s/%s", c.Host, c.Port, c.Database, c.Username)
}

// WithSchema returns a context with the app schema set.
func WithSchema(ctx context.Context, schema string) context.Context {
	return context.WithValue(ctx, schemaKey, schema)
}

// SchemaFrom reads the app schema from the context.
func SchemaFrom(ctx context.Context) string {
	s, _ := ctx.Value(schemaKey).(string)
	return s
}

// WithCredential returns a context with the per-app DB credential set.
// Used by the platform's Lambda invoke shim before calling the module
// handler, so Module.DB / Module.Tx can scope queries to the app schema.
func WithCredential(ctx context.Context, cred Credential) context.Context {
	return context.WithValue(ctx, credentialKey, &cred)
}

// CredentialFrom reads the per-app DB credential from the context.
func CredentialFrom(ctx context.Context) *Credential {
	c, _ := ctx.Value(credentialKey).(*Credential)
	return c
}

// WithModuleCredential returns a context with the per-module DB credential
// set. Used by the platform's Lambda invoke shim for handlers that touch
// the module's cross-app shared schema (mod_<id>). The use cases live on
// Module.ModuleDB; this function only handles credential injection.
func WithModuleCredential(ctx context.Context, cred Credential) context.Context {
	return context.WithValue(ctx, moduleCredentialKey, &cred)
}

// ModuleCredentialFrom reads the per-module DB credential from the context.
func ModuleCredentialFrom(ctx context.Context) *Credential {
	c, _ := ctx.Value(moduleCredentialKey).(*Credential)
	return c
}
