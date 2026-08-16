package core

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/mirrorstack-ai/app-module-sdk/internal/actor"
)

// Member identity assertions — the module-facing half of #518.
//
// WHO OWNS WHAT. The module that hosts the `auth-provider` slot owns the
// IDENTITY DECISION (whose session is this, is it still valid); the PLATFORM
// owns the IDENTITY CREDENTIAL other modules see. This file is the seam: the
// auth module says "I have decided this browser is member U of app A", the
// platform hands back a short-lived signed assertion, and dispatch turns that
// assertion back into ms.UserID(ctx) for every module downstream. The module
// never holds the signing key, and no other module ever holds a credential that
// can act AS the member.
//
// WHY THE PLATFORM NOTARISES INSTEAD OF EACH MODULE RESOLVING. The alternative
// — every consumer calls the auth module to turn a session into a user id —
// fails the audit rule structurally: `actor from ctx ONLY`. An actor a module
// fetched for itself is not an actor the platform can guarantee, so the one
// module that forgets writes an audit row claiming a system actor for a human
// action. It also hands every module a credential that can act as that member
// later, for anything (the confused deputy). Notarising keeps ms.UserID(ctx)
// uniformly true and keeps the member's own credential inside the auth module.
//
// 🔴 NO ROLE TRAVELS IN THE CLAIMS. Identity is not authorization. An assertion
// says WHO, never WHAT THEY MAY DO; every consuming module still asks its own
// policy question. That separation is what keeps a member from ever being
// mistaken for an operator by a module that only checked "is someone there".
//
// 🔴 INERT UNTIL PR 1 SHIPS. POST /apps/{appID}/member-assertions does not exist
// in any deployed platform as of this writing (it is PR 1 of #518). Every call
// here returns an error until it does — which is the correct shape: a mint that
// cannot happen must never look like a mint that produced an anonymous caller.

// memberAssertionEnvelopeVersion is the mint envelope version. The platform
// rejects an unknown version loudly rather than reading a future format as v1,
// so this must move in lockstep with the endpoint.
const memberAssertionEnvelopeVersion = 1

// maxAssertionLifetime is the sanity ceiling on the lifetime a mint response may
// claim. Deliberately far above the platform's 5-minute clamp — the SDK must not
// second-guess a policy dispatch owns, and a future longer clamp must not need
// an SDK release — and far below anything that could be mistaken for correct.
// Its job is to catch a BROKEN response (a unit mix-up, a garbage integer), not
// to enforce policy.
const maxAssertionLifetime = 24 * time.Hour

// memberAssertionUserID mirrors the platform's rule that an asserted principal
// is a UUID. The point is not formatting: it is that a module must not be able
// to assert a FREE-FORM principal — "admin", "*", or another module's internal
// key — through a field that is otherwise unconstrained. The platform re-checks
// after authenticating the sender; this copy exists to fail in the caller's own
// stack frame with no round trip, exactly like validateDeliveryPrefix.
var memberAssertionUserID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// MemberAssertion is ONE minted, platform-signed statement that a named member
// is acting. Present Token to a module's /public/* route as
// `Authorization: Bearer <token>` and dispatch verifies it and stamps the user
// id the same way it already does on /platform/*.
//
// UNLIKE DeliveryTicket, THE CREDENTIAL IS EXPORTED, and that is a real cost
// paid deliberately. A delivery ticket hides its token because the module's
// contract is "you get a URL"; an assertion exists precisely so the auth module
// can hand the string onward to the app's BFF, so there is nothing to hide it
// behind. DeliveryTicket therefore fails closed through TWO layers (response
// screening plus the armed check inside URL); this type has only the first.
// Wrapping Token in an accessor would be the same single layer with extra
// steps. So: the screening in newMemberAssertion is the whole guarantee, and it
// is pinned by its own test rather than only through the mint path.
//
// The zero value carries an empty Token and is therefore unusable — which is
// what every error path returns.
type MemberAssertion struct {
	// Token is the opaque platform-signed assertion. Treat it as a bearer
	// credential: anyone holding it can act as this member until it expires.
	Token string
	// ExpiresAt is when the assertion dies. After it, dispatch rejects the
	// request outright rather than downgrading it to anonymous.
	ExpiresAt time.Time
	// ExpiresIn is the CLAMPED lifetime the platform granted, which can be
	// shorter than the ttl proposed. A holder that renews on a schedule must
	// read THIS, not its own proposal — renewing off the proposal is how a
	// clamped credential expires mid-session.
	ExpiresIn time.Duration
}

// memberAssertionRequest is the mint envelope. Note what is ABSENT, and it is
// the same absence the delivery envelope has: no module id and no app id. The
// platform resolves the app from the URL and the SENDING MODULE from the
// credential, so there is no field here a caller could point at another app's
// members or use to impersonate another module's right to assert.
//
// TTLSeconds is a pointer so "unset" (platform default) is distinguishable on
// the wire from an explicit 0.
type memberAssertionRequest struct {
	V          int    `json:"v"`
	UserID     string `json:"userId"`
	TTLSeconds *int64 `json:"ttlSeconds,omitempty"`
}

type memberAssertionResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expiresAt"`
	ExpiresIn int64  `json:"expiresIn"`
}

// resolveMemberAssertionURL builds the platform-dispatch mint URL:
//
//	{base}/apps/{appID}/member-assertions
//
// base is MS_DISPATCH_URL with the host.docker.internal:8083 dev fallback when
// unset — the same resolution Call, Emit, Notify and Delivery use, via
// dispatchBaseFor so a task attempt's broker-attested routing wins over process
// state.
func resolveMemberAssertionURL(ctx context.Context, appID string) string {
	return fmt.Sprintf("%s/apps/%s/member-assertions", dispatchBaseFor(ctx), appID)
}

// MintMemberAssertion exchanges a decision this module already made — "this
// request belongs to member userID" — for a platform-signed assertion other
// modules can trust.
//
// ONLY THE MODULE HOSTING THE auth-provider SLOT MAY CALL THIS. The platform
// checks that at mint time against a fact recorded when the app was installed;
// any other module gets an error, whatever it sends. That check is the whole
// reason this endpoint is safe to expose at all: unlike a delivery token, which
// can only ever address the caller's own storage prefix, an identity assertion
// names SOMEBODY ELSE and has no naturally bounded scope.
//
// userID must be the member's UUID as the auth module knows it.
//
// ttl is a PROPOSAL. The platform clamps it (5 minutes today) and reports the
// result as ExpiresIn; a zero ttl takes the platform default. Propose no more
// than the remaining life of the session the assertion is derived from.
//
// Not module-bound (no *Module receiver), and deliberately so — same shape and
// same reason as Delivery and AppID: the platform derives the calling module
// from the CREDENTIAL, never from anything sent, and a receiver would falsely
// suggest a module id travels in the request.
//
// PACKAGE-LEVEL FUNCTION, NOT AN INTERFACE METHOD, and this is the deliberate
// answer to the PrefixDeleter-vs-Storer question. Storer's rule is that a method
// goes on the interface only when it is fundamental to what every holder of that
// interface does, and stays BESIDE it otherwise, because adding one is
// source-breaking for every implementation and test double outside the package.
// Minting an assertion is fundamental to nothing a module already holds: it is
// not a storage operation, not a cache operation, and it has no second
// implementation to abstract over — there is exactly one platform. Putting it on
// an interface would invent a seam whose only purpose is to let something OTHER
// than the platform mint identity, which is the one thing this design exists to
// prevent. It is a free function for the same reason Delivery is.
//
// 🔴 Fail closed. Every error path returns the zero MemberAssertion, whose
// Token is empty. There is no degraded return, because the degraded return here
// is the dangerous one: an empty or malformed token sent on a request makes
// every downstream module see an ANONYMOUS caller while the auth module believes
// it signed someone in — a member who is quietly served the signed-out answer,
// and an audit row that names no actor for a human action.
func MintMemberAssertion(ctx context.Context, userID string, ttl time.Duration) (MemberAssertion, error) {
	appID, err := appIDFromContext(ctx, "MintMemberAssertion")
	if err != nil {
		return MemberAssertion{}, err
	}
	if err := validateAssertedUserID(userID); err != nil {
		return MemberAssertion{}, fmt.Errorf("ms.MintMemberAssertion: %w", err)
	}
	ttlSeconds, err := dispatchTTLSeconds(ttl)
	if err != nil {
		return MemberAssertion{}, fmt.Errorf("ms.MintMemberAssertion: %w", err)
	}
	secret, err := outboundServiceSecret(ctx, "MintMemberAssertion")
	if err != nil {
		return MemberAssertion{}, err
	}

	var resp memberAssertionResponse
	if err := postDispatchJSONInto(ctx, "ms.MintMemberAssertion", resolveMemberAssertionURL(ctx, appID), appID,
		memberAssertionRequest{V: memberAssertionEnvelopeVersion, UserID: userID, TTLSeconds: ttlSeconds},
		&resp,
		map[string]string{"X-MS-Service-Secret": secret},
	); err != nil {
		return MemberAssertion{}, err
	}
	return newMemberAssertion(resp)
}

// newMemberAssertion turns a 200 into an assertion, or into an error.
//
// A 2xx is NOT on its own evidence that a usable credential came back. An empty
// token attached to a request is indistinguishable at the far end from no token
// at all, so a caller that trusted a hollow 200 would ship a signed-out
// experience to a signed-in member and never see an error. Everything below is
// therefore checked before a MemberAssertion exists at all.
//
// The token's SHAPE is deliberately not pinned. Delivery makes the same choice
// for the same reason: asserting the platform's token format here would couple
// the SDK to a token version, making a future msm2 a breaking SDK change, and
// dispatch is the authority that verifies signature, claims, expiry and app
// binding anyway. What must be true is only that the bytes survive the transport
// they will ride — an Authorization header — which is exactly the transport-only
// contract actor.ValidTransportValue already states for the equally opaque
// delegation token, so it is reused rather than re-derived. Its rejected set
// (empty, over 4096 bytes, and any of `, \t\r\n`) is the header-injection
// surface: a CR/LF would let a hostile or broken response append headers of its
// own to every request the assertion is attached to.
func newMemberAssertion(resp memberAssertionResponse) (MemberAssertion, error) {
	// 🔴 SCREEN THE VALUE THE CALLER ACTUALLY GETS, not the wire integer.
	// time.Duration is nanoseconds, so anything above ~9.2e9 seconds OVERFLOWS
	// and wraps NEGATIVE — and a platform that returned MILLISECONDS by mistake,
	// the single most common unit bug in a field shaped like this, passes a
	// `resp.ExpiresIn <= 0` screen and hands back a negative Duration. This
	// type's whole contract is "renew off ExpiresIn, not your own proposal", so
	// a negative value is either a timer that fires immediately (a renewal storm
	// against the mint endpoint) or a deadline already in the past. Both are a
	// hollow 200 that looks live, which is the one thing this function exists to
	// prevent.
	expiresIn := time.Duration(resp.ExpiresIn) * time.Second
	switch {
	case resp.Token == "":
		return MemberAssertion{}, fmt.Errorf("ms.MintMemberAssertion: platform returned no assertion")
	case !actor.ValidTransportValue(resp.Token):
		return MemberAssertion{}, fmt.Errorf("ms.MintMemberAssertion: platform returned an assertion that is not header-safe")
	case resp.ExpiresIn <= 0 || expiresIn <= 0:
		return MemberAssertion{}, fmt.Errorf("ms.MintMemberAssertion: platform returned a non-positive assertion lifetime (%ds)", resp.ExpiresIn)
	case expiresIn > maxAssertionLifetime:
		// A ceiling on top of the overflow guard, because a value can be absurd
		// without wrapping: 1e9 seconds is 31 years, positive, and still not a
		// lifetime any platform grants. The platform clamps to 5 minutes, so
		// anything past a day is a broken response, not a generous one.
		return MemberAssertion{}, fmt.Errorf("ms.MintMemberAssertion: platform returned an implausible assertion lifetime (%ds)", resp.ExpiresIn)
	}

	// expiresAt is a convenience, not the authority: expiresIn is the clamped
	// number the platform actually granted. An unparseable timestamp must not
	// fail a mint that succeeded, so derive it rather than reject.
	expiresAt, err := time.Parse(time.RFC3339, resp.ExpiresAt)
	if err != nil {
		expiresAt = time.Now().UTC().Add(expiresIn)
	}
	return MemberAssertion{
		Token:     resp.Token,
		ExpiresAt: expiresAt.UTC(),
		ExpiresIn: expiresIn,
	}, nil
}

// validateAssertedUserID keeps a free-form principal from ever reaching the
// wire. The platform re-checks after authenticating the sender — this copy
// exists to fail in the caller's own stack frame, not to be the boundary.
func validateAssertedUserID(userID string) error {
	switch {
	case userID == "":
		return fmt.Errorf("userID is required")
	case !memberAssertionUserID.MatchString(userID):
		return fmt.Errorf("userID %q must be a UUID; a module may not assert a free-form principal", userID)
	}
	return nil
}
