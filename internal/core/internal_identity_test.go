package core

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/mirrorstack-ai/app-module-sdk/auth"
)

// TestInternalRoute_TunnelForwarded_AppIDResolves is the end-to-end pin for
// the tunnel-Internal identity fix (ms-app-modules#30/#31): a request that
// dispatch forwards to an Internal-scope route on the dev/tunnel plane —
// valid secret plus strip-then-injected X-MS-* identity headers — must reach
// the handler with ms.AppID(r.Context()) resolving to the dispatch-injected
// app id (previously "" because internalAuth validated the secret but never
// promoted the headers).
func TestInternalRoute_TunnelForwarded_AppIDResolves(t *testing.T) {
	m := newTestModuleWithSecret(t, "media")
	var gotAppID string
	var gotIdentity *auth.Identity
	m.Internal(func(r chi.Router) {
		r.Get("/whoami", func(w http.ResponseWriter, r *http.Request) {
			gotAppID = AppID(r.Context())
			gotIdentity = auth.Get(r.Context())
			w.WriteHeader(http.StatusOK)
		})
	})

	// Shape of a dispatch tunnel forward: secret header + injected identity
	// triple (dev_tunnel.go strips all inbound X-MS-* then injects these).
	req := httptest.NewRequest("GET", "/internal/whoami", nil)
	req.Header.Set(auth.HeaderInternalSecret, "secret")
	req.Header.Set(auth.HeaderUserID, "u-owner-1")
	req.Header.Set(auth.HeaderAppID, "a-tunnel-1")
	req.Header.Set(auth.HeaderAppRole, auth.RoleAdmin)
	rec := httptest.NewRecorder()
	m.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body %q)", rec.Code, rec.Body.String())
	}
	if gotAppID != "a-tunnel-1" {
		t.Errorf("ms.AppID(ctx) = %q, want a-tunnel-1 (dispatch-injected app id)", gotAppID)
	}
	if gotIdentity == nil || gotIdentity.UserID != "u-owner-1" || gotIdentity.AppRole != auth.RoleAdmin {
		t.Errorf("identity mismatch: got %+v", gotIdentity)
	}

	// The unauthenticated twin (no secret, spoofed headers) must be rejected
	// before the handler — identity promotion never fires without a
	// validated secret.
	gotAppID, gotIdentity = "", nil
	spoof := httptest.NewRequest("GET", "/internal/whoami", nil)
	spoof.Header.Set(auth.HeaderAppID, "spoofed-app")
	rec2 := httptest.NewRecorder()
	m.Router().ServeHTTP(rec2, spoof)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without secret, got %d", rec2.Code)
	}
	if gotAppID != "" || gotIdentity != nil {
		t.Error("handler must not run (and no identity promote) on an unauthenticated request")
	}
}
