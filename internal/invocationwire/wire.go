// Package invocationwire implements the private transport representation of
// the public, read-only invocation context.
package invocationwire

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/mirrorstack-ai/app-module-sdk/ids"
	"github.com/mirrorstack-ai/app-module-sdk/internal/invocationstate"
	"github.com/mirrorstack-ai/app-module-sdk/invocation"
)

const (
	// Header carries raw-URL-base64 canonical invocation JSON on HTTP-shaped
	// transports. Authenticated SDK middleware consumes and removes it.
	Header = "X-MS-Invocation"

	// LegacyUserIDHeader projects the typed user during migration.
	LegacyUserIDHeader = "X-MS-User-ID"
	// LegacyIdentityKindHeader projects the typed identity namespace.
	LegacyIdentityKindHeader = "X-MS-Identity-Kind"
	// LegacyAppIDHeader projects the typed application scope.
	LegacyAppIDHeader = "X-MS-App-ID"
	// LegacyAppRoleHeader projects the typed platform role.
	LegacyAppRoleHeader = "X-MS-App-Role"
	// LegacyActorDelegationHeader projects the typed actor delegation.
	LegacyActorDelegationHeader = "X-MS-Actor-Delegation"
	// LegacyAllowedRedirectsHeader projects the typed redirect array.
	LegacyAllowedRedirectsHeader = "X-MS-App-Allowed-Redirects"
	// LegacyRequestIDHeader projects the typed request occurrence ID.
	LegacyRequestIDHeader = "X-Request-ID"
	auditProvenancePrefix = "mip1"
	cookiePrefixMarker    = "__msm1_"
	maxBytes              = 6 << 10
)

// WithContext installs a defensive copy under the SDK-private context key.
func WithContext(ctx context.Context, trusted invocation.Context) context.Context {
	trusted.Routes.Redirects = append([]string(nil), trusted.Routes.Redirects...)
	trusted.Cookies.Capabilities = append([]string(nil), trusted.Cookies.Capabilities...)
	return invocationstate.With(ctx, trusted)
}

// Marshal validates trusted and returns its canonical JSON representation.
func Marshal(trusted invocation.Context) ([]byte, error) {
	if err := Validate(trusted); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(trusted)
	if err != nil {
		return nil, fmt.Errorf("mirrorstack/invocation: marshal: %w", err)
	}
	if len(raw) > maxBytes {
		return nil, fmt.Errorf("mirrorstack/invocation: context exceeds %d bytes", maxBytes)
	}
	return raw, nil
}

// Parse strictly decodes canonical JSON. Unknown fields, trailing data,
// alternate whitespace/key order, and oversized values fail closed.
func Parse(raw []byte) (invocation.Context, error) {
	if len(raw) == 0 || len(raw) > maxBytes {
		return invocation.Context{}, errors.New("mirrorstack/invocation: invalid context size")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var trusted invocation.Context
	if err := decoder.Decode(&trusted); err != nil {
		return invocation.Context{}, fmt.Errorf("mirrorstack/invocation: decode: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return invocation.Context{}, errors.New("mirrorstack/invocation: trailing JSON value")
		}
		return invocation.Context{}, fmt.Errorf("mirrorstack/invocation: trailing data: %w", err)
	}
	if err := Validate(trusted); err != nil {
		return invocation.Context{}, err
	}
	canonical, err := json.Marshal(trusted)
	if err != nil {
		return invocation.Context{}, fmt.Errorf("mirrorstack/invocation: canonicalize: %w", err)
	}
	if !bytes.Equal(raw, canonical) {
		return invocation.Context{}, errors.New("mirrorstack/invocation: non-canonical context")
	}
	return trusted, nil
}

// EncodeHeader converts canonical invocation JSON to its HTTP header form.
func EncodeHeader(raw []byte) (string, error) {
	if _, err := Parse(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodeHeader parses the HTTP header form and returns both typed data and the
// exact canonical JSON bytes carried by the platform.
func DecodeHeader(value string) (invocation.Context, []byte, error) {
	if value == "" || len(value) > base64.RawURLEncoding.EncodedLen(maxBytes) {
		return invocation.Context{}, nil, errors.New("mirrorstack/invocation: invalid header size")
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != value {
		return invocation.Context{}, nil, errors.New("mirrorstack/invocation: malformed header encoding")
	}
	trusted, err := Parse(raw)
	if err != nil {
		return invocation.Context{}, nil, err
	}
	return trusted, raw, nil
}

// LegacyHeaders returns the exact bounded compatibility projection of trusted.
func LegacyHeaders(trusted invocation.Context) map[string]string {
	values := map[string]string{
		LegacyAppIDHeader:            trusted.App.ID,
		LegacyAllowedRedirectsHeader: strings.Join(trusted.Routes.Redirects, ","),
		LegacyRequestIDHeader:        trusted.Request.ID,
	}
	if trusted.Identity.UserID != "" {
		values[LegacyUserIDHeader] = trusted.Identity.UserID
	}
	if trusted.Identity.Kind != "" {
		values[LegacyIdentityKindHeader] = trusted.Identity.Kind
	}
	if trusted.Identity.AppRole != "" {
		values[LegacyAppRoleHeader] = trusted.Identity.AppRole
	}
	if trusted.Identity.ActorDelegation != "" {
		values[LegacyActorDelegationHeader] = trusted.Identity.ActorDelegation
	}
	return values
}

// LegacyHeaderNames lists every compatibility claim, including claims absent
// from an anonymous invocation.
func LegacyHeaderNames() []string {
	return []string{
		LegacyUserIDHeader,
		LegacyIdentityKindHeader,
		LegacyAppIDHeader,
		LegacyAppRoleHeader,
		LegacyActorDelegationHeader,
		LegacyAllowedRedirectsHeader,
		LegacyRequestIDHeader,
	}
}

// ValidateLegacyHeaders rejects compatibility values that conflict with the
// typed context. Absent values are permitted.
func ValidateLegacyHeaders(trusted invocation.Context, headers map[string]string) error {
	expected := LegacyHeaders(trusted)
	seen := make(map[string]struct{})
	for actualName, got := range headers {
		matched := ""
		for _, legacyName := range LegacyHeaderNames() {
			if strings.EqualFold(actualName, legacyName) {
				matched = legacyName
				break
			}
		}
		if matched == "" {
			continue
		}
		if _, duplicate := seen[matched]; duplicate {
			return fmt.Errorf("mirrorstack/invocation: duplicate legacy header %s", matched)
		}
		seen[matched] = struct{}{}
		want, expectedPresent := expected[matched]
		if !expectedPresent || got != want {
			return fmt.Errorf("mirrorstack/invocation: legacy header %s conflicts with typed context", matched)
		}
	}
	return nil
}

// BodyMatches reports whether body is the exact occurrence body bound into
// trusted.
func BodyMatches(trusted invocation.Context, body []byte) bool {
	digest := sha256.Sum256(body)
	return trusted.Request.BodySHA256 == hex.EncodeToString(digest[:])
}

// Validate checks every cross-field invariant shared with API Platform v1.
func Validate(trusted invocation.Context) error {
	if trusted.Version != invocation.Version {
		return fmt.Errorf("mirrorstack/invocation: unsupported version %d", trusted.Version)
	}
	if !canonicalUUID(trusted.App.ID) || !canonicalUUID(trusted.Module.ID) || !ids.ValidModuleSlug(trusted.Module.Slug) ||
		trusted.App.Schema != appSchemaName(trusted.App.ID) {
		return errors.New("mirrorstack/invocation: non-canonical authoritative scope")
	}
	if err := validateIdentity(trusted.Identity); err != nil {
		return err
	}
	if !canonicalUUID(trusted.Request.ID) || !validHTTPToken(trusted.Request.Method) ||
		!validPath(trusted.Request.Path) || trusted.Request.OccurredAt.IsZero() ||
		trusted.Request.OccurredAt.Location() != time.UTC {
		return errors.New("mirrorstack/invocation: invalid request context")
	}
	digest, err := hex.DecodeString(trusted.Request.BodySHA256)
	if err != nil || len(digest) != sha256.Size || strings.ToLower(trusted.Request.BodySHA256) != trusted.Request.BodySHA256 {
		return errors.New("mirrorstack/invocation: invalid body digest")
	}
	if trusted.Trust.Source != invocation.TrustSourceEdge && trusted.Trust.Source != invocation.TrustSourceDirect {
		return errors.New("mirrorstack/invocation: invalid trust source")
	}
	if trusted.Trust.ClientIP != "" && net.ParseIP(trusted.Trust.ClientIP) == nil {
		return errors.New("mirrorstack/invocation: invalid trusted client IP")
	}
	if trusted.Trust.Scheme != "http" && trusted.Trust.Scheme != "https" {
		return errors.New("mirrorstack/invocation: invalid trusted scheme")
	}
	if trusted.Trust.Host == "" || trusted.Trust.Host != strings.ToLower(strings.TrimSpace(trusted.Trust.Host)) ||
		trusted.Trust.Origin != trusted.Trust.Scheme+"://"+trusted.Trust.Host || trusted.Routes.Origin != trusted.Trust.Origin {
		return errors.New("mirrorstack/invocation: inconsistent trusted origin")
	}
	parsedOrigin, err := url.Parse(trusted.Trust.Origin)
	if err != nil || parsedOrigin.Scheme != trusted.Trust.Scheme || parsedOrigin.Host != trusted.Trust.Host ||
		parsedOrigin.User != nil || parsedOrigin.Path != "" || parsedOrigin.RawQuery != "" || parsedOrigin.Fragment != "" || parsedOrigin.Opaque != "" {
		return errors.New("mirrorstack/invocation: malformed trusted origin")
	}
	base := moduleRouteBase(trusted.App.ID, trusted.Module.Slug)
	if trusted.Routes.Module != base || trusted.Routes.Public != base+"/public" ||
		trusted.Routes.Platform != base+"/platform" || trusted.Routes.CurrentLocal != trusted.Request.Path {
		return errors.New("mirrorstack/invocation: inconsistent canonical routes")
	}
	if trusted.Routes.Redirects == nil {
		return errors.New("mirrorstack/invocation: redirects must be an array")
	}
	seenRedirects := make(map[string]struct{}, len(trusted.Routes.Redirects))
	for _, redirect := range trusted.Routes.Redirects {
		parsed, err := url.Parse(redirect)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
			return errors.New("mirrorstack/invocation: redirects are not canonical")
		}
		if _, duplicate := seenRedirects[redirect]; duplicate {
			return errors.New("mirrorstack/invocation: redirects contain duplicates")
		}
		seenRedirects[redirect] = struct{}{}
	}
	if trusted.Cookies.Namespace != cookieNamespace(trusted.App.ID, trusted.Module.ID) ||
		trusted.Cookies.PhysicalPrefix != cookiePrefix(trusted.App.ID, trusted.Module.ID) ||
		trusted.Cookies.LegacyReadUntil.IsZero() || trusted.Cookies.LegacyReadUntil.Location() != time.UTC ||
		len(trusted.Cookies.Capabilities) == 0 {
		return errors.New("mirrorstack/invocation: invalid cookie capabilities")
	}
	legacy, physical := false, false
	for index, capability := range trusted.Cookies.Capabilities {
		if capability != invocation.CookieCapabilityLegacyLogicalRead && capability != invocation.CookieCapabilityPhysicalNamesV1 {
			return errors.New("mirrorstack/invocation: unknown cookie capability")
		}
		legacy = legacy || capability == invocation.CookieCapabilityLegacyLogicalRead
		physical = physical || capability == invocation.CookieCapabilityPhysicalNamesV1
		if index > 0 && trusted.Cookies.Capabilities[index-1] >= capability {
			return errors.New("mirrorstack/invocation: cookie capabilities are not canonical")
		}
	}
	if !physical {
		return errors.New("mirrorstack/invocation: physical cookie capability missing")
	}
	if legacy && !trusted.Request.OccurredAt.Before(trusted.Cookies.LegacyReadUntil) {
		return errors.New("mirrorstack/invocation: legacy cookie capability is expired")
	}
	if !validAuditProvenance(trusted.Audit.Provenance) {
		return errors.New("mirrorstack/invocation: invalid audit provenance")
	}
	return nil
}

func validateIdentity(identity invocation.Identity) error {
	if identity.UserID == "" {
		if identity.Kind != "" || identity.AppRole != "" || identity.ActorDelegation != "" {
			return errors.New("mirrorstack/invocation: anonymous identity carries claims")
		}
		return nil
	}
	if identity.Kind != invocation.IdentityKindMember && identity.Kind != invocation.IdentityKindPlatform {
		return errors.New("mirrorstack/invocation: invalid identity namespace")
	}
	if identity.Kind == invocation.IdentityKindMember && (identity.AppRole != "" || identity.ActorDelegation != "") {
		return errors.New("mirrorstack/invocation: member identity carries platform claims")
	}
	if identity.Kind == invocation.IdentityKindPlatform && identity.AppRole == "" {
		return errors.New("mirrorstack/invocation: platform identity has no app role")
	}
	if identity.ActorDelegation != "" && identity.AppRole == "" {
		return errors.New("mirrorstack/invocation: actor delegation has no app role")
	}
	return nil
}

func canonicalUUID(value string) bool {
	return ids.ValidUUID(value) && value == strings.ToLower(value)
}

func appSchemaName(appID string) string {
	return "app_" + strings.ReplaceAll(appID, "-", "_")
}

func moduleRouteBase(appID, moduleSlug string) string {
	return "/v1/dispatch/apps/" + url.PathEscape(appID) + "/" + url.PathEscape(moduleSlug)
}

func cookieNamespace(appID, moduleID string) string {
	return "v1:" + appID + ":" + moduleID
}

func cookiePrefix(appID, moduleID string) string {
	digest := sha256.Sum256([]byte(appID + "\x00" + moduleID))
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(digest[:12])
	return cookiePrefixMarker + strings.ToLower(encoded) + "_"
}

func validPath(value string) bool {
	if !strings.HasPrefix(value, "/") || strings.Contains(value, "#") {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 0x20 || value[index] == 0x7f {
			return false
		}
	}
	return true
}

func validHTTPToken(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character <= 0x20 || character >= 0x7f || strings.ContainsRune("()<>@,;:\\\"/[]?={} \t", rune(character)) {
			return false
		}
	}
	return true
}

func validAuditProvenance(value string) bool {
	if len(value) > 4096 {
		return false
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] != auditProvenancePrefix {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(payload) == 0 || base64.RawURLEncoding.EncodeToString(payload) != parts[1] {
		return false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	return err == nil && len(signature) == sha256.Size && base64.RawURLEncoding.EncodeToString(signature) == parts[2]
}
