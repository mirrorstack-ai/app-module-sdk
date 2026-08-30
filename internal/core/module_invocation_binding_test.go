package core

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/internal/invocationstate"
)

func TestWithInvocationBindingInstallsModuleIdentityBeforeMiddleware(t *testing.T) {
	const moduleID = "m11111111111141118111111111111111"
	const moduleSlug = "user-core"

	var got invocationstate.Binding
	probe := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var ok bool
			got, ok = invocationstate.BindingFrom(r.Context())
			if !ok {
				t.Fatal("invocation binding was not installed before authentication")
			}
			next.ServeHTTP(w, r)
		})
	}
	handler := withInvocationBinding(moduleID, moduleSlug, probe)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusNoContent || got.ModuleID != moduleID || got.ModuleSlug != moduleSlug {
		t.Fatalf("status=%d binding=%+v", recorder.Code, got)
	}
}
