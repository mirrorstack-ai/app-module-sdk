// Package dispatchpath composes the URL a module uses to reach one platform
// dispatch surface: the configured dispatch base, the shared prefix, then the
// surface's own path.
//
// It exists so the prefix is written ONCE. meter cannot import internal/core
// (it is a public package and core imports it), so without a home both would
// carry their own copy of the same string — and a module whose ms.Record and
// ms.Emit disagreed about where dispatch lives would fail in only one of them,
// which is the hardest shape of this bug to see.
package dispatchpath

import "strings"

// Prefix is the path every module→dispatch surface is addressed under.
//
// 🔴 IT IS NOT DECORATION — IT IS WHAT MAKES THE ADDRESS INDEPENDENT OF THE
// BASE. On the deployed plane a module reaches dispatch through an API mapping
// key, and MS_DISPATCH_URL is baked into the module's Lambda environment when
// the module is PROVISIONED. So the base a running module carries is a snapshot
// of whatever the platform injected that day, and a module cannot be given a new
// one without being re-provisioned — the value lives in a PUBLISHED Lambda
// version's frozen environment, which an edit to $LATEST does not reach.
//
// While that snapshot was the bare api.<domain> host, the bare paths
// (/apps/{app}/usage and friends) had no route there, so every ms.Record,
// ms.Emit and ms.Notify from a deployed module 404'd — silently, because all
// three are fire-and-forget and the module's outbox quarantines after 8
// attempts. Measured on production 2026-09-11:
//
//	ms.Record /apps/<app>/usage -> 404: 404 page not found
//
// on user-core, whose app bill then read module_usage_total_micros = 0 across 12
// installed modules.
//
// /v1/dispatch is the one address that answers under BOTH bases: it is a path
// route on the bare host, and it survives the mapping key, which strips its own
// prefix. ms.Call already used it for exactly this reason after deployed
// module-to-module calls hit the same bug; this is that fix applied to the rest
// of the surface.
//
// The platform serves both this address and the bare paths (api-platform
// dispatchhandler.ModuleIngressDispatchPrefix), so an older module keeps
// working. THE PLATFORM SIDE MUST SHIP FIRST: a module that asks for this
// address before dispatch answers on it gets a 404, which is the very failure
// being fixed.
const Prefix = "/v1/dispatch"

// Join composes base + Prefix + path. base may carry a trailing slash or a
// mapping-key path of its own; path must start with "/".
func Join(base, path string) string {
	return strings.TrimRight(base, "/") + Prefix + path
}
