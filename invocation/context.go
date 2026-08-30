// Package invocation exposes the platform-authored context for one module
// request. The SDK installs a Context only after authenticating the platform
// transport; module code must never decode X-MS-Invocation itself.
package invocation

import (
	"context"
	"time"

	"github.com/mirrorstack-ai/app-module-sdk/internal/invocationstate"
)

const (
	// Version is the first canonical module-invocation contract.
	Version = 1

	// CookieCapabilityLegacyLogicalRead permits the platform to present an
	// unprefixed cookie only during the fixed compatibility window.
	CookieCapabilityLegacyLogicalRead = "legacy-logical-read"
	// CookieCapabilityPhysicalNamesV1 identifies the app-and-module-scoped
	// physical cookie namespace used for all current writes.
	CookieCapabilityPhysicalNamesV1 = "physical-names-v1"

	// IdentityKindMember identifies an end-user member assertion with no app
	// operator role.
	IdentityKindMember = "member"
	// IdentityKindPlatform identifies a platform user carrying an app role.
	IdentityKindPlatform = "platform"

	// TrustSourceDirect means API Platform received the request directly.
	TrustSourceDirect = "direct"
	// TrustSourceEdge means the trusted platform edge forwarded the request.
	TrustSourceEdge = "edge"
)

// Context is the transport-neutral, platform-authored context for one concrete
// request occurrence. Slices are defensively copied when stored in or loaded
// from a context.Context.
type Context struct {
	Version  int      `json:"v"`
	App      App      `json:"app"`
	Module   Module   `json:"module"`
	Identity Identity `json:"identity"`
	Routes   Routes   `json:"routes"`
	Request  Request  `json:"request"`
	Trust    Trust    `json:"trust"`
	Cookies  Cookies  `json:"cookies"`
	Audit    Audit    `json:"audit"`
}

// App is the authoritative application scope.
type App struct {
	ID     string `json:"id"`
	Schema string `json:"schema"`
}

// Module is the authoritative callee scope.
type Module struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
}

// Identity is the verified end-user namespace and platform role. Empty fields
// represent an actorless request.
type Identity struct {
	Kind            string `json:"kind,omitempty"`
	UserID          string `json:"userId,omitempty"`
	AppRole         string `json:"appRole,omitempty"`
	ActorDelegation string `json:"actorDelegation,omitempty"`
}

// Routes contains the canonical browser-facing routes for this installation.
type Routes struct {
	Origin       string   `json:"origin"`
	Module       string   `json:"module"`
	Public       string   `json:"public"`
	Platform     string   `json:"platform"`
	CurrentLocal string   `json:"currentLocal"`
	Redirects    []string `json:"redirects"`
}

// Request binds the context to one HTTP occurrence.
type Request struct {
	ID         string    `json:"id"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	BodySHA256 string    `json:"bodySha256"`
	OccurredAt time.Time `json:"occurredAt"`
}

// Trust records connection facts derived by the platform edge.
type Trust struct {
	Source   string `json:"source"`
	ClientIP string `json:"clientIp,omitempty"`
	Host     string `json:"host"`
	Scheme   string `json:"scheme"`
	Origin   string `json:"origin"`
}

// Cookies describes the platform-owned physical cookie namespace.
type Cookies struct {
	Namespace       string    `json:"namespace"`
	PhysicalPrefix  string    `json:"physicalPrefix"`
	Capabilities    []string  `json:"capabilities"`
	LegacyReadUntil time.Time `json:"legacyReadUntil"`
}

// Audit carries opaque occurrence-bound provenance. The SDK preserves it; the
// platform audit ingress owns signature verification.
type Audit struct {
	Provenance string `json:"provenance"`
}

// FromContext returns a defensive copy of the trusted invocation, if present.
func FromContext(ctx context.Context) (Context, bool) {
	trusted, ok := invocationstate.Value(ctx).(Context)
	if !ok {
		return Context{}, false
	}
	return trusted.clone(), true
}

func (trusted Context) clone() Context {
	trusted.Routes.Redirects = append([]string(nil), trusted.Routes.Redirects...)
	trusted.Cookies.Capabilities = append([]string(nil), trusted.Cookies.Capabilities...)
	return trusted
}
