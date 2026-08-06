// Package actor carries the dispatch-issued actor delegation between trusted
// SDK ingestion and outbound inter-module calls. It lives under internal so a
// module cannot manufacture a delegation through the public SDK surface.
package actor

import (
	"context"
	"strings"
)

// HeaderDelegation is the private dispatch-to-SDK wire header. Browser-facing
// code must never receive or set it; auth middleware captures it only after the
// platform proxy credential has been verified.
const HeaderDelegation = "X-MS-Actor-Delegation"

// MaxDelegationBytes bounds an opaque delegation before it is retained in a
// request context or forwarded on another hop. Current signed tokens are well
// below this limit.
const MaxDelegationBytes = 4096

type delegationKey struct{}
type pendingDelegationKey struct{}
type platformSurfaceKey struct{}

// ValidTransportValue applies only transport-level safety checks. Dispatch is
// the authority that verifies the signature, claims, expiry, and caller/app
// binding; the SDK deliberately does not duplicate that policy.
func ValidTransportValue(assertion string) bool {
	return assertion != "" && len(assertion) <= MaxDelegationBytes &&
		!strings.ContainsAny(assertion, ", \t\r\n")
}

// WithDelegation stores an already trusted opaque assertion in ctx.
func WithDelegation(ctx context.Context, assertion string) context.Context {
	if assertion == "" {
		return ctx
	}
	return context.WithValue(ctx, delegationKey{}, assertion)
}

// Delegation returns the private opaque assertion, or "" when this request
// has no end-user actor to delegate.
func Delegation(ctx context.Context) string {
	assertion, _ := ctx.Value(delegationKey{}).(string)
	return assertion
}

// WithPendingDelegation holds a trusted transport assertion until
// PlatformAuth confirms the request is on the platform surface. Public and
// Internal boundaries discard this private pending value before module code.
func WithPendingDelegation(ctx context.Context, assertion string) context.Context {
	if assertion == "" {
		return ctx
	}
	return context.WithValue(ctx, pendingDelegationKey{}, assertion)
}

// WithPlatformSurface marks the request as having entered through
// Module.Platform. The marker lives in this internal package so module code
// cannot manufacture the activation capability by nesting exported auth
// middleware inside a Public or Internal handler.
func WithPlatformSurface(ctx context.Context) context.Context {
	return context.WithValue(ctx, platformSurfaceKey{}, true)
}

// PlatformSurface reports whether Module.Platform marked this request.
func PlatformSurface(ctx context.Context) bool {
	marked, _ := ctx.Value(platformSurfaceKey{}).(bool)
	return marked
}

// WithoutDelegation shadows both active and pending delegation inherited from
// an upstream context. Public and Internal boundaries use it before module
// code runs, so nested exported middleware cannot recover a typed Lambda or
// proxy-captured assertion.
func WithoutDelegation(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, delegationKey{}, "")
	return context.WithValue(ctx, pendingDelegationKey{}, "")
}

// ActivatePendingDelegation promotes the private pending assertion only when
// the request carries Module.Platform's internal surface capability. It also
// shadows the pending value after promotion so there is one active state.
func ActivatePendingDelegation(ctx context.Context) context.Context {
	if !PlatformSurface(ctx) {
		return WithoutDelegation(ctx)
	}
	assertion, _ := ctx.Value(pendingDelegationKey{}).(string)
	ctx = WithoutDelegation(ctx)
	return WithDelegation(ctx, assertion)
}
