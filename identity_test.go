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

// TestIdentityAccessors_ContextReads pins the unit contract of ms.UserID and
// ms.AppRole against each identity state a context can carry.
func TestIdentityAccessors_ContextReads(t *testing.T) {
	cases := []struct {
		name               string
		ctx                context.Context
		wantUser, wantRole string
	}{
		{
			name: "no identity set returns empty strings",
			ctx:  context.Background(),
		},
		{
			name: "reads the fields from the context identity",
			ctx: auth.Set(context.Background(), auth.Identity{
				UserID:  "u-1",
				AppID:   "app-7",
				AppRole: auth.RoleAdmin,
			}),
			wantUser: "u-1",
			wantRole: auth.RoleAdmin,
		},
		{
			// Anonymous Public requests and internal/system/cron/task calls
			// carry an identity with no user — a valid state, not an error.
			name: "empty fields inside a set identity are legitimate",
			ctx:  auth.Set(context.Background(), auth.Identity{AppID: "app-7"}),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ms.UserID(tc.ctx); got != tc.wantUser {
				t.Errorf("UserID = %q, want %q", got, tc.wantUser)
			}
			if got := ms.AppRole(tc.ctx); got != tc.wantRole {
				t.Errorf("AppRole = %q, want %q", got, tc.wantRole)
			}
		})
	}
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

// TestIdentityAccessors_GuardHeaderPaths pins that all three accessors
// resolve identity ingested from validated trusted-forwarder headers (the dev
// tunnel / dispatch path) on both HTTP guard surfaces: Platform via
// PlatformAuth and Public via the proxy guard's validated-token path.
func TestIdentityAccessors_GuardHeaderPaths(t *testing.T) {
	cases := []struct {
		name           string
		internalSecret string // MS_INTERNAL_SECRET (empty avoids ambient cross-talk)
		platformToken  string // MS_PLATFORM_TOKEN (empty avoids ambient cross-talk)
		guard          func() func(http.Handler) http.Handler
		proofHeader    string // header proving trusted-forwarder status
		proofValue     string
		path           string
		user, app      string
	}{
		{
			name:           "platform surface via PlatformAuth",
			internalSecret: "test-secret",
			guard:          auth.PlatformAuth,
			proofHeader:    auth.HeaderInternalSecret,
			proofValue:     "test-secret",
			path:           "/platform/users",
			user:           "u-tunnel",
			app:            "app-tunnel",
		},
		{
			name:          "public surface via RequireProxy validated-token path",
			platformToken: "test-token",
			guard:         auth.RequireProxy,
			proofHeader:   auth.HeaderPlatformToken,
			proofValue:    "test-token",
			path:          "/public/me",
			user:          "u-pub",
			app:           "app-pub",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MS_INTERNAL_SECRET", tc.internalSecret)
			t.Setenv("MS_PLATFORM_TOKEN", tc.platformToken)

			var gotUser, gotApp, gotRole string
			handler := tc.guard()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				gotUser = ms.UserID(r.Context())
				gotApp = ms.AppID(r.Context())
				gotRole = ms.AppRole(r.Context())
			}))

			req := httptest.NewRequest("GET", tc.path, nil)
			req.Header.Set(tc.proofHeader, tc.proofValue)
			req.Header.Set(auth.HeaderUserID, tc.user)
			req.Header.Set(auth.HeaderAppID, tc.app)
			req.Header.Set(auth.HeaderAppRole, auth.RoleViewer)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", rec.Code)
			}
			if gotUser != tc.user || gotApp != tc.app || gotRole != auth.RoleViewer {
				t.Errorf("accessors = %q/%q/%q, want %s/%s/%s",
					gotUser, gotApp, gotRole, tc.user, tc.app, auth.RoleViewer)
			}
		})
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
