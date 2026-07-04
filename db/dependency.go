package db

import "context"

// DependencyGrant is one authorized cross-module read target the platform
// resolves at INVOKE time and ships down the trusted Lambda envelope
// (decision 18 §3). It is advisory ROUTING only — never authority: the
// install-time GRANT SELECT on the consumer's r_<app8>_<consumer> role is the
// single DB-level authorizer, and Postgres enforces it on the vended pool no
// matter what this manifest says.
//
//   - Ref is the producer keyed by the SAME normalized form the SDK's
//     parseProducerRef yields (the bare "slug" the platform reconstructs from
//     the producer's owner/slug — see decision 18 §3 step 6).
//   - Tables maps each exposed LOGICAL table name ("users") to the physical
//     relation the platform computed via ids.PhysicalTableName
//     ("m<hex>_users"). The SDK never derives the physical name; it reads it
//     here. Only tables exposed + consented on the producer's running version
//     appear.
//
// The JSON tags are the cross-repo wire contract with api-platform's local
// mirror (moduleinvoke.DependencyGrant — api-platform never imports the SDK).
// A rename on either side surfaces as a decode mismatch, not a silent drop.
type DependencyGrant struct {
	Ref    string            `json:"ref"`
	Tables map[string]string `json:"tables"`
}

const dependenciesKey = contextKey("ms-dependency-manifest")

// WithDependencies returns a context carrying the platform-resolved dependency
// manifest, parallel to WithSchema / WithCredential / WithPrefix. Set once by
// the Lambda invoke shim (runtime.InjectResources) from the trusted envelope;
// read by the deployed-plane DependencyDB branch. Never set from module input.
func WithDependencies(ctx context.Context, manifest []DependencyGrant) context.Context {
	return context.WithValue(ctx, dependenciesKey, manifest)
}

// DependenciesFrom reads the dependency manifest from the context. Returns nil
// when unset — which the deployed DependencyDB branch treats as "the platform
// does not inject a manifest yet" and fails closed to today's dev-plane-only
// error (the rollout gate: decision 18 §3 read step 1).
func DependenciesFrom(ctx context.Context) []DependencyGrant {
	m, _ := ctx.Value(dependenciesKey).([]DependencyGrant)
	return m
}
