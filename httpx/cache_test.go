package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/httpx"
)

func TestNoStoreSetsHeadersBeforeCallingNext(t *testing.T) {
	t.Parallel()

	handler := httpx.NoStore(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if got := w.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("Cache-Control inside handler = %q", got)
		}
		if got := w.Header().Get("Pragma"); got != "no-cache" {
			t.Fatalf("Pragma inside handler = %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}
