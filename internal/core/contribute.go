package core

import (
	"encoding/json"
	"fmt"

	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
)

// ContributesTo declares that this module pushes payload INTO another module's
// contribution slot — the contributor side of Provide. It is a
// ZERO-RUNTIME declaration that mirrors DependsOn: the payload becomes manifest
// metadata (contributesTo), and the platform catalog — or the CLI dev runner in
// local dev — performs the actual registration into the host after app-owner
// approval. The module's binary never calls the host directly.
//
// hostSpec takes the same id or id@constraint form as DependsOn, and for the
// same reason: a contribution couples you to the host's declared SLOT surface
// exactly as a dependency couples you to its API, and a host may reshape or
// remove a slot on any version bump — the pinned range is what lets the catalog
// refuse a contribution against a host version that no longer declares the
// slot. Pair it with ms.DependsOn(hostSpec) so the platform installs the host
// first and can validate the slot against the host's provides.
//
// payload must be a JSON-serializable value matching the host slot's declared
// type (e.g. oauth-core's AuthProvider). Re-declaring the same (host, slot)
// replaces the payload and the constraint — last call wins.
//
// Panics on an empty host/slot, a host id or SemVer constraint that does not
// parse, or an unmarshalable payload — all programmer error, caught at startup
// rather than shipping a manifest entry nobody can resolve.
//
//	ms.ContributesTo("oauth-core@^1.2", "auth-provider", AuthProvider{
//	    Slug: "google", Name: "Google", Icon: "login",
//	    AuthorizeURL: "/authorize-url", CallbackURL: "/callback",
//	})
func (m *Module) ContributesTo(hostSpec, slot string, payload any) {
	if hostSpec == "" {
		panic("ms.ContributesTo: host is required")
	}
	if slot == "" {
		panic("ms.ContributesTo: slot is required")
	}
	host, constraint := parseDepSpec(hostSpec)
	registry.ValidateDepID("ContributesTo", host)
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(fmt.Sprintf("ms.ContributesTo(%q, %q): marshal payload: %v", host, slot, err))
	}
	m.registry.AddOutboundContribution(registry.OutboundContribution{
		Host:       host,
		Constraint: constraint,
		Slot:       slot,
		Payload:    raw,
	})
}

// ContributesTo declares an outbound contribution on the default module. Panics
// if Init has not been called. See Module.ContributesTo.
func ContributesTo(hostSpec, slot string, payload any) {
	mustDefault("ContributesTo").ContributesTo(hostSpec, slot, payload)
}
