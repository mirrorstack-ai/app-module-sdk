// Package sdktest contains test-only helpers for module contract tests.
package sdktest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"github.com/mirrorstack-ai/app-module-sdk/system"
)

// TB is the subset of testing.TB used by this package.
type TB interface {
	Helper()
	Fatalf(format string, args ...any)
}

// Manifest requests and decodes the SDK manifest exposed by handler.
// internalSecret must match the module's configured internal credential.
func Manifest(t TB, handler http.Handler, internalSecret string) system.ManifestPayload {
	t.Helper()

	request := httptest.NewRequest(http.MethodGet, "/__mirrorstack/platform/manifest", nil)
	if internalSecret != "" {
		request.Header.Set("X-MS-Internal-Secret", internalSecret)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("manifest status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var payload system.ManifestPayload
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	return payload
}
