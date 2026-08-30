package sdktest_test

import (
	"net/http"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/sdktest"
)

func TestManifestRequestsAuthenticatedPlatformEndpoint(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/__mirrorstack/platform/manifest" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("X-MS-Internal-Secret"); got != "test-secret" {
			t.Fatalf("internal secret = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"defaults":{"name":"Example"}}`))
	})

	manifest := sdktest.Manifest(t, handler, "test-secret")
	if manifest.Defaults.Name != "Example" {
		t.Fatalf("manifest defaults = %+v", manifest.Defaults)
	}
}
