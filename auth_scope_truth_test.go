// These are permanent CHARACTERIZATION tests of the SDK's current auth model.
// They assert what the implementation does today, not what it ought to do.
// Three separate design documents written in one day asserted the opposite:
// they treated Internal scope as a trust boundary and described Platform and
// Internal auth as disjoint domains. Each assertion was disproven by a
// throwaway probe test, but each probe was then deleted, allowing the next
// author to repeat the same mistake. This file is that probe made permanent.
// Its purpose is to let CI contradict an inaccurate document or refactor
// instead of relying on a reviewer's memory of the current security model.
// Changing ANY assertion here is a deliberate SECURITY decision, not routine
// test fixup. The affected design documents must change in the same PR.
// Some pinned behavior is undesirable; individual test comments say so where
// relevant. Those observations do not weaken the assertions: every test still
// pins the SDK's CURRENT behavior, warts included.
package mirrorstack_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	ms "github.com/mirrorstack-ai/app-module-sdk"
	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/internal/runtime"
	"github.com/mirrorstack-ai/app-module-sdk/roles"
)

func setCharacterizationAuthSecret(t *testing.T, internalSecret string) {
	t.Helper()
	t.Setenv("MS_PLATFORM_TOKEN_FILE", "")
	t.Setenv("MS_PLATFORM_TOKEN", "")
	t.Setenv("MS_INTERNAL_SECRET", internalSecret)
	t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "")
}

func clearCharacterizationModuleEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("MS_LOCAL_DB_URL", "")
	t.Setenv("MS_DISPATCH_URL", "")
}

// TestCharacterization_InternalSecretSatisfiesPlatformAuth_ScopesAreNotDisjoint
// pins auth/middleware.go: PlatformAuth and InternalAuth read the same secret
// source. Undesirably, Internal is not a separate credential domain or trust
// boundary: one X-MS-Internal-Secret authenticates both scopes.
func TestCharacterization_InternalSecretSatisfiesPlatformAuth_ScopesAreNotDisjoint(t *testing.T) {
	setCharacterizationAuthSecret(t, "shared-secret")
	clearCharacterizationModuleEnvironment(t)

	scopes := []struct {
		name       string
		middleware func() func(http.Handler) http.Handler
	}{
		{name: "PlatformAuth", middleware: auth.PlatformAuth},
		{name: "InternalAuth", middleware: auth.InternalAuth},
	}

	for _, scope := range scopes {
		t.Run(scope.name, func(t *testing.T) {
			handler := scope.middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			for _, tc := range []struct {
				name       string
				secret     string
				wantStatus int
			}{
				{name: "shared internal secret is accepted", secret: "shared-secret", wantStatus: http.StatusOK},
				{name: "wrong internal secret is rejected", secret: "WRONG", wantStatus: http.StatusUnauthorized},
			} {
				t.Run(tc.name, func(t *testing.T) {
					req := httptest.NewRequest(http.MethodGet, "/ping", nil)
					req.Header.Set(auth.HeaderInternalSecret, tc.secret)
					req.Header.Set(auth.HeaderUserID, "u-scope-overlap")
					req.Header.Set(auth.HeaderAppID, "a-scope-overlap")
					req.Header.Set(auth.HeaderAppRole, auth.RoleViewer)
					rec := httptest.NewRecorder()

					handler.ServeHTTP(rec, req)

					if rec.Code != tc.wantStatus {
						t.Fatalf("status = %d, want %d (body %q)", rec.Code, tc.wantStatus, rec.Body.String())
					}
				})
			}
		})
	}

	// On the real Platform surface, the internal secret is honored at two
	// independent admission points: requireProxy validates it and promotes the
	// forwarded X-MS-* headers into an identity, then platformAuth admits via its
	// step-1 upstream-identity branch without reaching its own secret check.
	// Mutating platformAuth alone to require the platform-token header still
	// leaves this route at 200; only mutating requireProxy as well makes it return
	// 403 not_proxied. Thus the internal secret is a valid credential at both the
	// proxy guard and platform-auth layers of the Platform surface: "Internal is
	// a separate trust boundary" is false at two independent places, not one.
	m, err := ms.New(ms.Config{
		ID:   "auth_scope_overlap",
		Slug: "scope-overlap",
		Name: "Auth scope overlap characterization",
	})
	if err != nil {
		t.Fatalf("ms.New: %v", err)
	}
	m.Platform(func(r chi.Router) {
		r.Get("/ping", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	})

	for _, tc := range []struct {
		name       string
		secret     string
		wantStatus int
		wantCode   string
	}{
		{name: "correct internal secret is accepted", secret: "shared-secret", wantStatus: http.StatusOK},
		{name: "wrong internal secret is rejected by proxy guard", secret: "WRONG", wantStatus: http.StatusForbidden, wantCode: auth.CodeNotProxied},
	} {
		t.Run("real Platform surface/"+tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/platform/ping", nil)
			req.Header.Set(auth.HeaderInternalSecret, tc.secret)
			req.Header.Set(auth.HeaderUserID, "u-platform-surface")
			req.Header.Set(auth.HeaderAppID, "a-platform-surface")
			req.Header.Set(auth.HeaderAppRole, auth.RoleViewer)
			rec := httptest.NewRecorder()
			m.Router().ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantCode != "" {
				var body struct {
					Code string `json:"code"`
				}
				if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
					t.Fatalf("decode error response: %v (body %q)", err, rec.Body.String())
				}
				if body.Code != tc.wantCode {
					t.Errorf("error code = %q, want %q", body.Code, tc.wantCode)
				}
			}
		})
	}
}

// TestCharacterization_RequirePermissionAdmitsAdminRegardlessOfDeclaredRoles
// pins internal/core/module.go's unconditional admin injection. An actor with
// AppRole=admin passes every RequirePermission route in the SDK, whatever the
// permission declares; a permission can never be made admin-excluded.
func TestCharacterization_RequirePermissionAdmitsAdminRegardlessOfDeclaredRoles(t *testing.T) {
	setCharacterizationAuthSecret(t, "")
	clearCharacterizationModuleEnvironment(t)

	m, err := ms.New(ms.Config{
		ID:   "permission_admin_injected",
		Slug: "perm-admin",
		Name: "Permission admin characterization",
	})
	if err != nil {
		t.Fatalf("ms.New: %v", err)
	}
	m.RegisterPermission("moderate", ms.PermissionOpts{DefaultRole: roles.Custom("moderator")})
	handler := m.RequirePermission("moderate")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	cases := []struct {
		role       string
		wantStatus int
		wantBody   string
	}{
		{role: roles.Admin().Key, wantStatus: http.StatusOK},
		{role: roles.Custom("moderator").Key, wantStatus: http.StatusOK},
		{role: roles.Viewer().Key, wantStatus: http.StatusForbidden, wantBody: "{\"error\":\"forbidden\"}\n"},
		{role: auth.RoleMember, wantStatus: http.StatusForbidden, wantBody: "{\"error\":\"forbidden\"}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/moderate", nil)
			req = req.WithContext(auth.Set(req.Context(), auth.Identity{
				UserID:  "u-permission",
				AppID:   "a-permission",
				AppRole: tc.role,
			}))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if rec.Body.String() != tc.wantBody {
				t.Errorf("body = %q, want %q", rec.Body.String(), tc.wantBody)
			}
		})
	}
}

// TestCharacterization_UndeclaredPermissionIsAdminOnly_AdminStillPasses pins
// internal/core/module.go's lazy fallback for an unknown permission name:
// RequirePermission registers it admin-only, admitting admin and denying viewer.
func TestCharacterization_UndeclaredPermissionIsAdminOnly_AdminStillPasses(t *testing.T) {
	setCharacterizationAuthSecret(t, "")
	clearCharacterizationModuleEnvironment(t)

	m, err := ms.New(ms.Config{
		ID:   "undeclared_permission_admin",
		Slug: "undeclared",
		Name: "Undeclared permission characterization",
	})
	if err != nil {
		t.Fatalf("ms.New: %v", err)
	}
	handler := m.RequirePermission("never.declared")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, tc := range []struct {
		role       string
		wantStatus int
	}{
		{role: roles.Admin().Key, wantStatus: http.StatusOK},
		{role: roles.Viewer().Key, wantStatus: http.StatusForbidden},
	} {
		t.Run(tc.role, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/undeclared", nil)
			req = req.WithContext(auth.Set(req.Context(), auth.Identity{
				UserID:  "u-undeclared",
				AppID:   "a-undeclared",
				AppRole: tc.role,
			}))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

// TestCharacterization_RoleGateReturns401WithoutActor_403OnRoleMiss pins
// auth/permission.go's status and body split. An identity with an empty
// AppRole gets 401, not 403: "authenticated" here means "has a role" even
// though an identity object is present.
func TestCharacterization_RoleGateReturns401WithoutActor_403OnRoleMiss(t *testing.T) {
	setCharacterizationAuthSecret(t, "")
	clearCharacterizationModuleEnvironment(t)

	m, err := ms.New(ms.Config{
		ID:   "role_gate_status_split",
		Slug: "role-split",
		Name: "Role gate status characterization",
	})
	if err != nil {
		t.Fatalf("ms.New: %v", err)
	}
	m.RegisterPermission("view", ms.PermissionOpts{DefaultRole: roles.Viewer()})
	handler := m.RequirePermission("view")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	cases := []struct {
		name       string
		identity   *auth.Identity
		wantStatus int
		wantBody   string
	}{
		{
			name:       "no identity",
			wantStatus: http.StatusUnauthorized,
			wantBody:   "{\"error\":\"authentication required\"}\n",
		},
		{
			name:       "identity with empty app role",
			identity:   &auth.Identity{UserID: "u-empty-role", AppID: "a-empty-role"},
			wantStatus: http.StatusUnauthorized,
			wantBody:   "{\"error\":\"authentication required\"}\n",
		},
		{
			name:       "identity with nonmatching member role",
			identity:   &auth.Identity{UserID: "u-member", AppID: "a-member", AppRole: auth.RoleMember},
			wantStatus: http.StatusForbidden,
			wantBody:   "{\"error\":\"forbidden\"}\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/view", nil)
			if tc.identity != nil {
				req = req.WithContext(auth.Set(req.Context(), *tc.identity))
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if rec.Body.String() != tc.wantBody {
				t.Errorf("body = %q, want %q", rec.Body.String(), tc.wantBody)
			}
		})
	}
}

// TestCharacterization_DeployedEnvelopeIsPayloadTrusted_InternalSecretNeverChecked
// pins internal/runtime/lambda.go and auth/middleware.go: a Lambda envelope is
// payload-trusted before InternalAuth runs, so no internal secret is consulted.
// Undesirably, the same wrong secret rejected on the HTTP plane above passes on
// the deployed plane, meaning the two planes do not share an auth gate.
func TestCharacterization_DeployedEnvelopeIsPayloadTrusted_InternalSecretNeverChecked(t *testing.T) {
	setCharacterizationAuthSecret(t, "shared-secret")
	clearCharacterizationModuleEnvironment(t)

	m, err := ms.New(ms.Config{
		ID:   "deployed_payload_trust",
		Slug: "payload-trust",
		Name: "Deployed payload trust characterization",
	})
	if err != nil {
		t.Fatalf("ms.New: %v", err)
	}

	var observedIdentity *auth.Identity
	m.Internal(func(r chi.Router) {
		r.Get("/ping", func(w http.ResponseWriter, r *http.Request) {
			if id := auth.Get(r.Context()); id != nil {
				copyOfID := *id
				observedIdentity = &copyOfID
			}
			w.WriteHeader(http.StatusOK)
		})
	})
	invoke := runtime.NewLambdaHandler(m.Router())

	cases := []struct {
		name         string
		headers      map[string]string
		userID       string
		appID        string
		appRole      string
		wantIdentity *auth.Identity
	}{
		{
			name:    "no secret header",
			headers: nil,
		},
		{
			name: "wrong secret header",
			headers: map[string]string{
				auth.HeaderInternalSecret: "TOTALLY-WRONG",
			},
		},
		{
			name: "typed payload identity overrides spoofed identity header",
			headers: map[string]string{
				auth.HeaderAppRole: auth.RoleAdmin,
			},
			userID:  "u-payload",
			appID:   "a-payload",
			appRole: auth.RoleViewer,
			wantIdentity: &auth.Identity{
				UserID:  "u-payload",
				AppID:   "a-payload",
				AppRole: auth.RoleViewer,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			observedIdentity = nil
			payload, err := json.Marshal(runtime.LambdaRequest{
				Method:  http.MethodGet,
				Path:    "/internal/ping",
				Headers: tc.headers,
				UserID:  tc.userID,
				AppID:   tc.appID,
				AppRole: tc.appRole,
			})
			if err != nil {
				t.Fatalf("marshal LambdaRequest: %v", err)
			}

			resp, err := invoke(context.Background(), payload)
			if err != nil {
				t.Fatalf("lambda handler: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %q)", resp.StatusCode, resp.Body)
			}
			if tc.wantIdentity == nil {
				return
			}
			if observedIdentity == nil {
				t.Fatal("handler saw no identity, want typed payload identity")
			}
			if *observedIdentity != *tc.wantIdentity {
				t.Errorf("handler identity = %+v, want %+v", *observedIdentity, *tc.wantIdentity)
			}
		})
	}
}

// TestCharacterization_ForwarderIdentityTrustedOnlyOnSecretValidatedPath pins
// promoteForwarderIdentity in auth/middleware.go: identity headers become an
// auth.Identity only after secret validation, never on local bypass or reject.
func TestCharacterization_ForwarderIdentityTrustedOnlyOnSecretValidatedPath(t *testing.T) {
	addIdentityHeaders := func(req *http.Request) {
		req.Header.Set(auth.HeaderUserID, "u1")
		req.Header.Set(auth.HeaderAppID, "a1")
		req.Header.Set(auth.HeaderAppRole, auth.RoleAdmin)
	}

	t.Run("correct secret promotes the whole identity", func(t *testing.T) {
		setCharacterizationAuthSecret(t, "shared-secret")

		var observed *auth.Identity
		reached := false
		handler := auth.InternalAuth()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reached = true
			if id := auth.Get(r.Context()); id != nil {
				copyOfID := *id
				observed = &copyOfID
			}
			w.WriteHeader(http.StatusOK)
		}))
		req := httptest.NewRequest(http.MethodGet, "/internal", nil)
		req.Header.Set(auth.HeaderInternalSecret, "shared-secret")
		addIdentityHeaders(req)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
		}
		if !reached {
			t.Fatal("handler was not reached")
		}
		want := auth.Identity{UserID: "u1", AppID: "a1", AppRole: auth.RoleAdmin}
		if observed == nil {
			t.Fatal("handler saw no identity")
		}
		if *observed != want {
			t.Errorf("handler identity = %+v, want %+v", *observed, want)
		}
	})

	t.Run("unconfigured local bypass does not trust identity headers", func(t *testing.T) {
		setCharacterizationAuthSecret(t, "")

		var observed *auth.Identity
		reached := false
		handler := auth.InternalAuth()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reached = true
			observed = auth.Get(r.Context())
			w.WriteHeader(http.StatusOK)
		}))
		req := httptest.NewRequest(http.MethodGet, "/internal", nil)
		addIdentityHeaders(req)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
		}
		if !reached {
			t.Fatal("handler was not reached")
		}
		if observed != nil {
			t.Errorf("handler identity = %+v, want nil because bypass did not validate the headers", *observed)
		}
	})

	t.Run("wrong secret rejects before identity promotion", func(t *testing.T) {
		setCharacterizationAuthSecret(t, "shared-secret")

		reached := false
		handler := auth.InternalAuth()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			reached = true
			w.WriteHeader(http.StatusOK)
		}))
		req := httptest.NewRequest(http.MethodGet, "/internal", nil)
		req.Header.Set(auth.HeaderInternalSecret, "WRONG")
		addIdentityHeaders(req)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401 (body %q)", rec.Code, rec.Body.String())
		}
		if reached {
			t.Fatal("handler was reached after wrong-secret rejection")
		}
	})
}
