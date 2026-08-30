package httpx

import (
	"strings"
	"testing"
)

func TestValidStaticModuleRoute(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		route   string
		surface ModuleSurface
		want    bool
	}{
		{name: "internal child", route: "/internal/profile", surface: InternalSurface, want: true},
		{name: "nested platform child", route: "/platform/users/profile-card", surface: PlatformSurface, want: true},
		{name: "public child", route: "/public/avatar.svg", surface: PublicSurface, want: true},
		{name: "exact byte limit", route: "/internal/" + strings.Repeat("a", 2038), surface: InternalSurface, want: true},
		{name: "wrong surface", route: "/platform/profile", surface: InternalSurface},
		{name: "unknown surface", route: "/internal/profile", surface: ModuleSurface("unknown")},
		{name: "surface root", route: "/internal", surface: InternalSurface},
		{name: "surface root slash", route: "/internal/", surface: InternalSurface},
		{name: "trailing slash", route: "/internal/profile/", surface: InternalSurface},
		{name: "query", route: "/internal/profile?full=1", surface: InternalSurface},
		{name: "fragment", route: "/internal/profile#top", surface: InternalSurface},
		{name: "absolute URL", route: "https://example.test/internal/profile", surface: InternalSurface},
		{name: "scheme-shaped path", route: "/internal/https://example.test", surface: InternalSurface},
		{name: "braced template", route: "/internal/users/{id}", surface: InternalSurface},
		{name: "colon template", route: "/internal/users/:id", surface: InternalSurface},
		{name: "wildcard template", route: "/internal/users/*", surface: InternalSurface},
		{name: "bracket template", route: "/internal/users/[id]", surface: InternalSurface},
		{name: "angle template", route: "/internal/users/<id>", surface: InternalSurface},
		{name: "encoded separator", route: "/internal/users%2Fadmin", surface: InternalSurface},
		{name: "backslash", route: `/internal/users\admin`, surface: InternalSurface},
		{name: "dot traversal", route: "/internal/users/../admin", surface: InternalSurface},
		{name: "dot segment", route: "/internal/users/./profile", surface: InternalSurface},
		{name: "repeated separator", route: "/internal/users//profile", surface: InternalSurface},
		{name: "invalid UTF-8", route: "/internal/\xff", surface: InternalSurface},
		{name: "too long", route: "/internal/" + strings.Repeat("a", 2048), surface: InternalSurface},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := ValidStaticModuleRoute(test.route, test.surface); got != test.want {
				t.Errorf("ValidStaticModuleRoute(%q, %q) = %v, want %v", test.route, test.surface, got, test.want)
			}
		})
	}
}
