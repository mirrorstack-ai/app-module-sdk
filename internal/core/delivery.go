package core

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Gated CDN delivery — the module-facing half.
//
// WHO OWNS WHAT. The module owns the ENTITLEMENT DECISION (was the credit
// charged, is the role held); the PLATFORM owns the DELIVERY CREDENTIAL. This
// file is the seam between them: the module says "I have decided this viewer
// may watch job J", the platform hands back a short-lived, prefix-scoped URL,
// and cdn-worker verifies it at the edge. The module never sees, holds, or
// derives the signing key — it receives a URL and nothing else, which is why
// DeliveryTicket keeps the token in an UNEXPORTED field.
//
// WHY THIS EXISTS AT ALL. Serving a segment from a presigned S3 URL makes the
// bytes uncacheable by construction: a fresh signature per request is a fresh
// cache key per request, so every viewer pays a full S3 GET plus egress for
// every segment. A delivery token rides the QUERY STRING, and cdn-worker's
// cache key is the path only — so one cached object serves every entitled
// viewer, and the credential does not fragment the cache.
//
// 🔴 THERE IS NO UNSIGNED FALLBACK, ANYWHERE IN THIS FILE. Every failure path
// returns ("", err). Returning a bare CDN URL when minting fails would look
// exactly like a working feature while serving paywalled content to anyone with
// the link — and because those URLs land in viewers' playlists, it is not a
// mistake a rollback can take back.

// GatedKeyPrefix is the module-relative key prefix that opts an object into
// token gating. A module writes gated objects under it and reads them back at
// the same key, so the key it stores and the key it later serves are identical.
//
// The edge's rule is broader than this (any "/"-delimited segment equal to
// "_gated" is gated) while the platform only ever MINTS for keys of the exact
// apps/<app>/<module>/_gated/… shape. That asymmetry is deliberate and fails
// closed: a creatively-nested key becomes unreachable, never public.
const GatedKeyPrefix = "_gated/"

// deliveryEnvelopeVersion is the mint envelope version. The platform rejects an
// unknown version loudly rather than reading a future format as v1, so this
// must move in lockstep with the endpoint.
const deliveryEnvelopeVersion = 1

// maxDeliveryPrefixLen bounds the module-relative prefix so the COMPOSED prefix
// stays inside the platform's 512-byte ceiling. The platform prepends
// "apps/<uuid>/<uuid>/" — 5 + 36 + 1 + 36 + 1 bytes — and that composed string
// is signed into the token, which lands in every segment URL of every playlist
// line. Checking here turns a guaranteed 400 into a local error with no round
// trip.
const maxDeliveryPrefixLen = 512 - (len("apps/") + 36 + len("/") + 36 + len("/"))

// deliveryKeyCharset is the allowlist for every caller-supplied byte that ends
// up in a signed prefix or in a URL path. It is the same charset the platform
// enforces on the composed prefix.
//
// 🔴 THIS IS WHAT CLOSES DELIMITER INJECTION LOCALLY. The signed canonical
// string is LF-delimited, and storage.validateKey does NOT reject "\n" (it
// rejects only empty, leading '/', ".." and '%'). The platform re-checks the
// composed prefix before signing and the edge checks it again after verifying,
// so a hole here is not exploitable — but a module that discovers its newline
// key was silently rejected 400 rounds later has been failed by this SDK, not
// helped by it.
var deliveryKeyCharset = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// deliveryParamPattern guards the query-parameter NAME the platform returns.
// The name is returned rather than hardcoded here so a future rename is a
// platform-only change — which means an unvalidated value would let a broken or
// hostile response inject extra query structure into every segment URL.
var deliveryParamPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// deliveryTokenCharset keeps the token to bytes that are safe verbatim in a
// query value. It is deliberately NOT the token's two-part base64url shape:
// pinning the shape here would recreate the coupling that returning `param`
// exists to avoid. It only has to be true that the token needs no escaping, so
// the bytes the platform signed are the bytes the edge verifies.
var deliveryTokenCharset = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)

// DeliveryTicket is ONE minted delivery credential together with the CDN origin
// it is valid against. One ticket covers every object under Prefix, so a caller
// with many keys — a transcode job is hundreds of segments across four
// renditions — mints ONCE and calls URL for each. Minting per segment would
// spend one platform round trip per playlist line.
//
// The credential itself is unexported on purpose: the module's contract is "you
// get a URL", not "you get a token you may attach yourself". There is no
// accessor, so there is no call site that can build an almost-right URL.
type DeliveryTicket struct {
	// Prefix is the platform-composed ABSOLUTE object-key prefix this ticket
	// authorises: apps/<app>/<module>/_gated/…/. It is echoed by the platform so
	// the module can assert what it actually got instead of reconstructing it.
	Prefix string
	// URLPrefix is the CDN origin joined to Prefix. The module never
	// concatenates the CDN base itself — that string is platform configuration
	// and the module must not hold a second opinion about it.
	URLPrefix string
	// ExpiresAt is when the credential dies. After it, the edge returns 403 and
	// the viewer needs a re-mint (which is what re-fetching the playlist does).
	ExpiresAt time.Time
	// ExpiresIn is the CLAMPED lifetime the platform granted. It can be shorter
	// than the ttl proposed — the proposal is a proposal — so a caller that
	// wants to re-mint on schedule must read this rather than assume its own.
	ExpiresIn time.Duration

	param string
	token string
}

// URL returns the delivery URL for one object, addressed RELATIVE to the
// ticket's prefix. relativeKey is the tail: for a ticket on
// "_gated/videos/v1/transcodes/j1/720p/" that is "seg_000042.ts".
//
// It never returns a URL without the credential attached — a ticket that
// somehow reached a caller unarmed is an error, not a public URL.
func (t DeliveryTicket) URL(relativeKey string) (string, error) {
	if t.token == "" || t.param == "" || t.URLPrefix == "" {
		return "", fmt.Errorf("ms.DeliveryTicket.URL: ticket is not armed (mint it with ms.Delivery)")
	}
	if err := validateDeliveryRelativeKey(relativeKey); err != nil {
		return "", fmt.Errorf("ms.DeliveryTicket.URL: %w", err)
	}
	return t.URLPrefix + relativeKey + "?" + t.param + "=" + t.token, nil
}

// deliveryMintRequest is the mint envelope. Note what is ABSENT: no module id
// and no app id. The platform resolves the app from the URL and the module from
// the credential, so there is no field here a caller could point at somebody
// else's storage.
//
// TTLSeconds is a pointer so "unset" (platform default) is distinguishable on
// the wire from an explicit 0.
type deliveryMintRequest struct {
	V          int    `json:"v"`
	Prefix     string `json:"prefix"`
	TTLSeconds *int64 `json:"ttlSeconds,omitempty"`
}

type deliveryMintResponse struct {
	Token     string `json:"token"`
	Param     string `json:"param"`
	Prefix    string `json:"prefix"`
	URLPrefix string `json:"urlPrefix"`
	ExpiresAt string `json:"expiresAt"`
	ExpiresIn int64  `json:"expiresIn"`
}

// resolveDeliveryMintURL builds the platform-dispatch mint URL:
//
//	{base}/apps/{appID}/cdn-tokens
//
// base is MS_DISPATCH_URL with the host.docker.internal:8083 dev fallback when
// unset — the same resolution Call, Emit and Notify use. DEV/DISPATCH
// TRANSPORT: this resolver is the #146 seam, mirroring resolveEventBusURL.
func resolveDeliveryMintURL(ctx context.Context, appID string) string {
	return fmt.Sprintf("%s/apps/%s/cdn-tokens", dispatchBaseFor(ctx), appID)
}

// Delivery mints ONE delivery credential covering every object under prefix.
//
// prefix is relative to this module's own storage prefix — exactly the key
// space ms.Storage operates in — and must start with GatedKeyPrefix and end
// with "/". Scope it to the narrowest thing the entitlement decision actually
// covered: for video that is one transcode job, which is one video across all
// its renditions. Do not scope it at "_gated/": that is "every private object
// this module owns", which is a bucket token wearing a smaller hat.
//
// ttl is a PROPOSAL. The platform clamps it into its own bounds and reports the
// result as ExpiresIn; a zero ttl takes the platform default. Propose no more
// than the remaining lifetime of the entitlement the caller is acting on — a
// delivery credential outliving the entitlement that produced it is strictly
// worse than the presigned URL it replaces.
//
// Not module-bound (no *Module receiver), and deliberately so: the platform
// derives the calling module from the CREDENTIAL, never from anything sent. A
// receiver would falsely suggest a module id travels in the request. Same shape
// and same reason as AppID.
//
// 🔴 Fail closed. Every error path returns the zero ticket, which cannot
// produce a URL. There is no degraded mode.
func Delivery(ctx context.Context, prefix string, ttl time.Duration) (DeliveryTicket, error) {
	appID, err := appIDFromContext(ctx, "Delivery")
	if err != nil {
		return DeliveryTicket{}, err
	}
	if err := validateDeliveryPrefix(prefix); err != nil {
		return DeliveryTicket{}, fmt.Errorf("ms.Delivery: %w", err)
	}
	ttlSeconds, err := dispatchTTLSeconds(ttl)
	if err != nil {
		return DeliveryTicket{}, fmt.Errorf("ms.Delivery: %w", err)
	}
	secret, err := outboundServiceSecret(ctx, "Delivery")
	if err != nil {
		return DeliveryTicket{}, err
	}

	var resp deliveryMintResponse
	if err := postDispatchJSONInto(ctx, "ms.Delivery", resolveDeliveryMintURL(ctx, appID), appID,
		deliveryMintRequest{V: deliveryEnvelopeVersion, Prefix: prefix, TTLSeconds: ttlSeconds},
		&resp,
		map[string]string{"X-MS-Service-Secret": secret},
	); err != nil {
		return DeliveryTicket{}, err
	}
	return newDeliveryTicket(resp, prefix)
}

// newDeliveryTicket turns a 200 into a ticket, or into an error.
//
// A 2xx is NOT on its own evidence that a usable credential came back. An empty
// token, a blank parameter name or a half-built origin would each produce a URL
// that looks right and carries no credential — the "non-empty placeholder
// defeats an is-it-empty gate" failure, except the placeholder here is the
// empty string and the consequence is a public paywalled segment. Everything
// below is therefore checked before a ticket exists at all.
func newDeliveryTicket(resp deliveryMintResponse, requestedPrefix string) (DeliveryTicket, error) {
	switch {
	case resp.Token == "":
		return DeliveryTicket{}, fmt.Errorf("ms.Delivery: platform returned no delivery token")
	case !deliveryTokenCharset.MatchString(resp.Token):
		return DeliveryTicket{}, fmt.Errorf("ms.Delivery: platform returned a delivery token that is not query-safe")
	case !deliveryParamPattern.MatchString(resp.Param):
		return DeliveryTicket{}, fmt.Errorf("ms.Delivery: platform returned an unusable token parameter name %q", resp.Param)
	case resp.ExpiresIn <= 0:
		return DeliveryTicket{}, fmt.Errorf("ms.Delivery: platform returned a non-positive token lifetime (%ds)", resp.ExpiresIn)
	case !strings.HasSuffix(resp.Prefix, "/"):
		return DeliveryTicket{}, fmt.Errorf("ms.Delivery: platform returned a composed prefix that is not a prefix: %q", resp.Prefix)
	// The composed prefix must END with what was asked for. A prefix that does
	// not is a BROADER or DIFFERENT authorisation than the entitlement decision
	// covered, and silently building URLs against it would widen the scope of
	// every one of them.
	case !strings.HasSuffix(resp.Prefix, requestedPrefix):
		return DeliveryTicket{}, fmt.Errorf("ms.Delivery: platform composed prefix %q does not cover the requested %q", resp.Prefix, requestedPrefix)
	case !strings.HasPrefix(resp.URLPrefix, "http://") && !strings.HasPrefix(resp.URLPrefix, "https://"):
		return DeliveryTicket{}, fmt.Errorf("ms.Delivery: platform returned no usable CDN origin (%q)", resp.URLPrefix)
	// urlPrefix is CDN origin + "/" + prefix. If it does not end in the very
	// prefix the token authorises, URLs built from it address objects the token
	// does not cover and the edge 403s every one of them.
	case !strings.HasSuffix(resp.URLPrefix, resp.Prefix):
		return DeliveryTicket{}, fmt.Errorf("ms.Delivery: platform CDN url %q does not end in the composed prefix %q", resp.URLPrefix, resp.Prefix)
	}

	expiresIn := time.Duration(resp.ExpiresIn) * time.Second
	// expiresAt is a convenience, not the authority: expiresIn is the clamped
	// number the platform actually granted. An unparseable timestamp must not
	// fail a mint that succeeded, so derive it rather than reject.
	expiresAt, err := time.Parse(time.RFC3339, resp.ExpiresAt)
	if err != nil {
		expiresAt = time.Now().UTC().Add(expiresIn)
	}
	return DeliveryTicket{
		Prefix:    resp.Prefix,
		URLPrefix: resp.URLPrefix,
		ExpiresAt: expiresAt.UTC(),
		ExpiresIn: expiresIn,
		param:     resp.Param,
		token:     resp.Token,
	}, nil
}

// DeliveryURL returns the CDN delivery URL for ONE gated object, with a freshly
// minted credential attached. key is relative to this module's storage prefix
// and must live under GatedKeyPrefix.
//
// It mints per call, scoped to the key's own directory. For many keys under one
// prefix — a playlist's worth of segments — call Delivery once and use the
// returned ticket, which is one platform round trip instead of hundreds.
//
// 🔴 An error means NO URL. It never degrades to the unsigned CDN URL and never
// falls back to a presigned S3 URL: an unsigned URL for gated content is a
// paywall bypass that looks like a working feature.
func DeliveryURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	prefix, name, err := splitDeliveryKey(key)
	if err != nil {
		return "", fmt.Errorf("ms.DeliveryURL: %w", err)
	}
	ticket, err := Delivery(ctx, prefix, ttl)
	if err != nil {
		return "", err
	}
	return ticket.URL(name)
}

// splitDeliveryKey cuts a module-relative gated key into the prefix a token is
// minted for and the object name within it.
func splitDeliveryKey(key string) (prefix, name string, err error) {
	if err := validateDeliveryRelativeKey(key); err != nil {
		return "", "", err
	}
	// The gating requirement is enforced in ONE place — validateDeliveryPrefix on
	// the derived prefix. An ungated key ("videos/v1/seg.ts") and a lookalike
	// ("_gatedx/…") both fail there, and a key with no directory at all derives
	// the empty prefix, which fails there too. A second copy of the same check
	// here would only give two ways to disagree about what "gated" means.
	cut := strings.LastIndex(key, "/")
	prefix, name = key[:cut+1], key[cut+1:]
	if err := validateDeliveryPrefix(prefix); err != nil {
		return "", "", err
	}
	return prefix, name, nil
}

// validateDeliveryPrefix mirrors the platform's allowlist on the composed
// prefix, applied to the module-relative half. The platform re-checks after
// composing and the edge re-checks after verifying the signature — this copy
// exists to fail in the caller's own stack frame, not to be the boundary.
func validateDeliveryPrefix(prefix string) error {
	switch {
	case prefix == "":
		return fmt.Errorf("prefix is required")
	case !strings.HasPrefix(prefix, GatedKeyPrefix):
		return fmt.Errorf("prefix %q must start with %q", prefix, GatedKeyPrefix)
	case !strings.HasSuffix(prefix, "/"):
		return fmt.Errorf("prefix %q must end with '/'", prefix)
	case len(prefix) > maxDeliveryPrefixLen:
		return fmt.Errorf("prefix exceeds %d bytes", maxDeliveryPrefixLen)
	case strings.Contains(prefix, ".."):
		return fmt.Errorf("prefix %q must not contain '..'", prefix)
	case strings.Contains(prefix, "//"):
		return fmt.Errorf("prefix %q must not contain '//'", prefix)
	case !deliveryKeyCharset.MatchString(prefix):
		return fmt.Errorf("prefix %q must contain only [A-Za-z0-9._/-]", prefix)
	}
	return nil
}

// validateDeliveryRelativeKey guards the bytes appended to a CDN URL path. The
// charset is an allowlist rather than an escape step on purpose: the edge
// matches the DECODED key against the signed prefix, so keeping the wire bytes
// identical to the key bytes removes a whole class of "it encoded differently
// on one side" failures.
func validateDeliveryRelativeKey(key string) error {
	switch {
	case key == "":
		return fmt.Errorf("key is required")
	case strings.HasPrefix(key, "/"):
		return fmt.Errorf("key %q must not start with '/'", key)
	case strings.HasSuffix(key, "/"):
		return fmt.Errorf("key %q must name an object, not a prefix", key)
	case strings.Contains(key, ".."):
		return fmt.Errorf("key %q must not contain '..'", key)
	case strings.Contains(key, "//"):
		return fmt.Errorf("key %q must not contain '//'", key)
	case !deliveryKeyCharset.MatchString(key):
		return fmt.Errorf("key %q must contain only [A-Za-z0-9._/-]", key)
	}
	return nil
}
