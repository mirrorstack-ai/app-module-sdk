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

// WithPendingDelegation holds a proxy-validated HTTP assertion until
// PlatformAuth confirms the request is on the platform surface. Public routes
// may carry this private pending value but cannot forward it through ms.Call.
func WithPendingDelegation(ctx context.Context, assertion string) context.Context {
	if assertion == "" {
		return ctx
	}
	return context.WithValue(ctx, pendingDelegationKey{}, assertion)
}

// ActivatePendingDelegation promotes the private pending assertion for
// server-side calls made by a platform handler.
func ActivatePendingDelegation(ctx context.Context) context.Context {
	assertion, _ := ctx.Value(pendingDelegationKey{}).(string)
	return WithDelegation(ctx, assertion)
}
