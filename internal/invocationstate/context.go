// Package invocationstate owns the private context key shared by the SDK's
// authenticated transport consumers and the public read-only invocation API.
package invocationstate

import "context"

type contextKey struct{}
type bindingKey struct{}

// Binding is the module identity that authenticated middleware must match.
// It is internal so modules cannot weaken or replace the binding selected by
// core.Module construction.
type Binding struct {
	ModuleID   string
	ModuleSlug string
}

// With stores value under an SDK-private key. The internal package boundary is
// intentional: module code may read the platform-authored invocation through
// invocation.FromContext, but cannot install a forged one.
func With(ctx context.Context, value any) context.Context {
	return context.WithValue(ctx, contextKey{}, value)
}

// Value returns the value stored under the SDK-private invocation key.
func Value(ctx context.Context) any {
	return ctx.Value(contextKey{})
}

// WithBinding stores the running module identity before authentication.
func WithBinding(ctx context.Context, binding Binding) context.Context {
	return context.WithValue(ctx, bindingKey{}, binding)
}

// BindingFrom returns the running module identity, when core installed one.
func BindingFrom(ctx context.Context) (Binding, bool) {
	binding, ok := ctx.Value(bindingKey{}).(Binding)
	return binding, ok
}
