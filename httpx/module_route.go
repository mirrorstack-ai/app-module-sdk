package httpx

import (
	"net/url"
	pathpkg "path"
	"strings"
	"unicode/utf8"
)

const maxStaticModuleRouteBytes = 2048

// ModuleSurface names one SDK-authenticated HTTP surface.
type ModuleSurface string

const (
	// InternalSurface is the actorless service-to-service module surface.
	InternalSurface ModuleSurface = "internal"
	// PlatformSurface is the operator-authenticated module surface.
	PlatformSurface ModuleSurface = "platform"
	// PublicSurface is the module surface reachable without an operator role.
	PublicSurface ModuleSurface = "public"
)

// ValidStaticModuleRoute reports whether raw is one concrete, module-relative
// route under surface, such as "/internal/profile".
//
// Contribution payloads carry callable endpoints, not router declarations, so
// roots, trailing slashes, parameters/templates, queries, fragments, escaped
// bytes, backslashes, and dot traversal are all rejected. The 2 KiB bound
// matches the contract boundary before the value is persisted.
func ValidStaticModuleRoute(raw string, surface ModuleSurface) bool {
	prefix, ok := moduleSurfacePrefix(surface)
	if !ok || len(raw) > maxStaticModuleRouteBytes || !utf8.ValidString(raw) ||
		!strings.HasPrefix(raw, prefix) || strings.HasSuffix(raw, "/") ||
		strings.ContainsAny(raw, `\%?#{}*:<[]>`) {
		return false
	}

	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return parsed.Path == raw && parsed.EscapedPath() == raw && pathpkg.Clean(raw) == raw
}

func moduleSurfacePrefix(surface ModuleSurface) (string, bool) {
	switch surface {
	case InternalSurface:
		return "/internal/", true
	case PlatformSurface:
		return "/platform/", true
	case PublicSurface:
		return "/public/", true
	default:
		return "", false
	}
}
