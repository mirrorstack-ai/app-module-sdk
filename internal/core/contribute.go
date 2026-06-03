package core

import (
	"encoding/json"
	"fmt"

	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
)

// ContributesTo declares that this module pushes payload INTO another module's
// contribution slot — the contributor side of DefineContribute. It is a
// ZERO-RUNTIME declaration that mirrors DependsOn: the payload becomes manifest
// metadata (contributesTo), and the platform catalog — or the CLI dev runner in
// local dev — performs the actual registration into the host after app-owner
// approval. The module's binary never calls the host directly.
//
// Pair it with ms.DependsOn(host) so the platform installs the host first and
// can validate the slot exists against the host's definedContributions.
//
// payload must be a JSON-serializable value matching the host slot's declared
// type (e.g. oauth-core's AuthProvider). Re-declaring the same (host, slot)
// replaces the payload — last call wins.
//
// Panics on empty host/slot or an unmarshalable payload — programmer error,
// caught at startup before serving.
//
//	ms.ContributesTo("oauth-core", "auth-provider", AuthProvider{
//	    Slug: "google", Name: "Google", Icon: "login",
//	    AuthorizeURL: "/authorize-url", CallbackURL: "/callback",
//	})
func (m *Module) ContributesTo(host, slot string, payload any) {
	if host == "" {
		panic("ms.ContributesTo: host is required")
	}
	if slot == "" {
		panic("ms.ContributesTo: slot is required")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(fmt.Sprintf("ms.ContributesTo(%q, %q): marshal payload: %v", host, slot, err))
	}
	m.registry.AddOutboundContribution(registry.OutboundContribution{
		Host:    host,
		Slot:    slot,
		Payload: raw,
	})
}

// ContributesTo declares an outbound contribution on the default module. Panics
// if Init has not been called. See Module.ContributesTo.
func ContributesTo(host, slot string, payload any) {
	mustDefault("ContributesTo").ContributesTo(host, slot, payload)
}
