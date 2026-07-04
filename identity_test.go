package mirrorstack_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	ms "github.com/mirrorstack-ai/app-module-sdk"
	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/internal/runtime"
)

func TestUserID(t *testing.T) {
	t.Run("returns empty string when no identity is set", func(t *testing.T) {
		if got := ms.UserID(context.Background()); got != "" {
			t.Errorf("UserID = %q, want empty string", got)
		}
	})

	t.Run("reads the user id from the context identity", func(t *testing.T) {
		ctx := auth.Set(context.Background(), auth.Identity{
			UserID:  "u-1",
			AppID:   "app-7",
			AppRole: auth.RoleAdmin,
		})
		if got := ms.UserID(ctx); got != "u-1" {
			t.Errorf("UserID = %q, want u-1", got)
		}
	})

	t.Run("empty user id inside a set identity is legitimate", func(t *testing.T) {
		// Anonymous Public requests and internal/system calls carry an
		// identity with no user — that is a valid state, not an error.
		ctx := auth.Set(context.Background(), auth.Identity{AppID: "app-7"})
		if got := ms.UserID(ctx); got != "" {
			t.Errorf("UserID = %q, want empty string", got)
		}
	})
}

func TestAppRole(t *testing.T) {
	t.Run("returns empty string when no identity is set", func(t *testing.T) {
		if got := ms.AppRole(context.Background()); got != "" {
			t.Errorf("AppRole = %q, want empty string", got)
		}
	})

	t.Run("reads the app role from the context identity", func(t *testing.T) {
		ctx := auth.Set(context.Background(), auth.Identity{
			UserID:  "u-1",
			AppID:   "app-7",
			AppRole: auth.RoleAdmin,
		})
		if got := ms.AppRole(ctx); got != auth.RoleAdmin {
			t.Errorf("AppRole = %q, want %q", got, auth.RoleAdmin)
		}
	})

	t.Run("empty role inside a set identity is legitimate", func(t *testing.T) {
		// Internal/system/cron/task invocations carry no user role.
		ctx := auth.Set(context.Background(), auth.Identity{AppID: "app-7"})
		if got := ms.AppRole(ctx); got != "" {
			t.Errorf("AppRole = %q, want empty string", got)
		}
	})
}

// TestIdentityAccessors_EnvelopeInjectPath pins that all three accessors
// resolve identity injected by runtime.InjectResources — the shared trusted
// gate the Lambda handler AND the task worker feed from the typed envelope.
func TestIdentityAccessors_EnvelopeInjectPath(t *testing.T) {
	ctx, err := runtime.InjectResources(context.Background(), runtime.InjectParams{
		UserID:  "u-envelope",
		AppID:   "app-envelope",
		AppRole: auth.RoleMember,
	})
	if err != nil {
		t.Fatalf("InjectResources: %v", err)
	}

	if got := ms.UserID(ctx); got != "u-envelope" {
		t.Errorf("UserID = %q, want u-envelope", got)
	}
	if got := ms.AppID(ctx); got != "app-envelope" {
		t.Errorf("AppID = %q, want app-envelope", got)
	}
	if got := ms.AppRole(ctx); got != auth.RoleMember {
		t.Errorf("AppRole = %q, want %q", got, auth.RoleMember)
	}
}

// TestIdentityAccessors_PlatformAuthHeaderPath pins that all three accessors
// resolve identity the PLATFORM surface ingests from validated trusted-
// forwarder headers (the dev tunnel / dispatch path).
func TestIdentityAccessors_PlatformAuthHeaderPath(t *testing.T) {
	t.Setenv("MS_PLATFORM_TOKEN", "")
	t.Setenv("MS_INTERNAL_SECRET", "test-secret")

	var gotUser, gotApp, gotRole string
	handler := auth.PlatformAuth()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotUser = ms.UserID(r.Context())
		gotApp = ms.AppID(r.Context())
		gotRole = ms.AppRole(r.Context())
	}))

	req := httptest.NewRequest("GET", "/platform/users", nil)
	req.Header.Set(auth.HeaderInternalSecret, "test-secret")
	req.Header.Set(auth.HeaderUserID, "u-tunnel")
	req.Header.Set(auth.HeaderAppID, "app-tunnel")
	req.Header.Set(auth.HeaderAppRole, auth.RoleViewer)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if gotUser != "u-tunnel" || gotApp != "app-tunnel" || gotRole != auth.RoleViewer {
		t.Errorf("accessors = %q/%q/%q, want u-tunnel/app-tunnel/%q",
			gotUser, gotApp, gotRole, auth.RoleViewer)
	}
}

// TestIdentityAccessors_RequireProxyHeaderPath pins that all three accessors
// resolve identity the PUBLIC surface ingests on the proxy guard's
// validated-token path (the dev tunnel / dispatch path).
func TestIdentityAccessors_RequireProxyHeaderPath(t *testing.T) {
	t.Setenv("MS_INTERNAL_SECRET", "") // avoid ambient env cross-talk
	t.Setenv("MS_PLATFORM_TOKEN", "test-token")

	var gotUser, gotApp, gotRole string
	handler := auth.RequireProxy()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotUser = ms.UserID(r.Context())
		gotApp = ms.AppID(r.Context())
		gotRole = ms.AppRole(r.Context())
	}))

	req := httptest.NewRequest("GET", "/public/me", nil)
	req.Header.Set(auth.HeaderPlatformToken, "test-token")
	req.Header.Set(auth.HeaderUserID, "u-pub")
	req.Header.Set(auth.HeaderAppID, "app-pub")
	req.Header.Set(auth.HeaderAppRole, auth.RoleViewer)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if gotUser != "u-pub" || gotApp != "app-pub" || gotRole != auth.RoleViewer {
		t.Errorf("accessors = %q/%q/%q, want u-pub/app-pub/%q",
			gotUser, gotApp, gotRole, auth.RoleViewer)
	}
}

// TestIdentityAccessors_DeployedPath_HeadersStrippedCtxResolves pins the
// ms-app-modules#30 footgun as a test: on the deployed Lambda path the shim
// STRIPS the client-settable X-MS-* identity headers, so a module that reads
// r.Header.Get(auth.Header*) gets "" — while ms.UserID / ms.AppID / ms.AppRole
// still resolve the trusted identity from the typed invoke payload. Reading
// headers works on the tunnel and silently breaks deployed; the ctx accessors
// work on both.
func TestIdentityAccessors_DeployedPath_HeadersStrippedCtxResolves(t *testing.T) {
	var (
		headerUser, headerApp, headerRole string
		ctxUser, ctxApp, ctxRole          string
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/whoami", func(w http.ResponseWriter, r *http.Request) {
		headerUser = r.Header.Get(auth.HeaderUserID)
		headerApp = r.Header.Get(auth.HeaderAppID)
		headerRole = r.Header.Get(auth.HeaderAppRole)
		ctxUser = ms.UserID(r.Context())
		ctxApp = ms.AppID(r.Context())
		ctxRole = ms.AppRole(r.Context())
	})

	handler := runtime.NewLambdaHandler(mux)
	payload, err := json.Marshal(runtime.LambdaRequest{
		Method: "GET",
		Path:   "/whoami",
		// Spoofed identity claims riding the forwarded headers — the shim
		// must drop every one of them.
		Headers: map[string]string{
			auth.HeaderUserID:  "spoofed-user",
			auth.HeaderAppID:   "spoofed-app",
			auth.HeaderAppRole: auth.RoleAdmin,
		},
		// Trusted identity — typed envelope fields injected by the platform.
		UserID:  "u-payload",
		AppID:   "app-payload",
		AppRole: auth.RoleMember,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	resp, err := handler(context.Background(), payload)
	if err != nil {
		t.Fatalf("lambda handler: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d (body %q)", resp.StatusCode, resp.Body)
	}

	if headerUser != "" || headerApp != "" || headerRole != "" {
		t.Errorf("X-MS-* identity headers must be stripped on the deployed path, got %q/%q/%q",
			headerUser, headerApp, headerRole)
	}
	if ctxUser != "u-payload" {
		t.Errorf("UserID = %q, want u-payload (typed payload identity)", ctxUser)
	}
	if ctxApp != "app-payload" {
		t.Errorf("AppID = %q, want app-payload (typed payload identity)", ctxApp)
	}
	if ctxRole != auth.RoleMember {
		t.Errorf("AppRole = %q, want %q (typed payload identity)", ctxRole, auth.RoleMember)
	}
}
