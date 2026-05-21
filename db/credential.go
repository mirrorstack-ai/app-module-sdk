package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

type contextKey string

const (
	schemaKey           = contextKey("ms-app-schema")
	prefixKey           = contextKey("ms-table-prefix")
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

// WithPrefix returns a context with the module's resolved table prefix set.
// The platform's Lambda invoke shim populates this from
// app_<app_id>.module_install.prefix per request, so the SDK can resolve a
// module's per-app table names without a fresh catalog round-trip on each
// call. Empty prefix means "no resolution available" — callers fall back
// to the dev/legacy mod_<id> form.
//
// Distinct from WithSchema: schema is the Postgres search_path
// (app_<app_id>); prefix is the leading segment baked into per-app table
// names inside that schema (<username>_<slug>_<table>).
func WithPrefix(ctx context.Context, prefix string) context.Context {
	return context.WithValue(ctx, prefixKey, prefix)
}

// PrefixFrom reads the module's resolved table prefix from the context.
// Returns the empty string when unset.
func PrefixFrom(ctx context.Context) string {
	p, _ := ctx.Value(prefixKey).(string)
	return p
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

// EnvBaseCredential returns the Host/Port/Database derived from the SDK's
// standard dev env vars (resolved via devEnvURL — same ladder db.Open
// consults). Username and Token are left empty for the caller to fill
// from per-invocation credentials — e.g. the install request body carries
// a per-(app, module) username + token but the DB location it should
// connect to comes from this helper.
//
// Production (deployed runtime) will replace this with a managed-secret
// fetch when that infra lands; the contract (returns a partial Credential
// the caller completes with per-invocation fields) stays identical so the
// install handler doesn't change.
func EnvBaseCredential() (Credential, error) {
	cfg, err := pgxpool.ParseConfig(devEnvURL())
	if err != nil {
		return Credential{}, fmt.Errorf("mirrorstack/db: parse env URL: %w", err)
	}
	cc := cfg.ConnConfig
	return Credential{Host: cc.Host, Port: int(cc.Port), Database: cc.Database}, nil
}
