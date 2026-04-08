package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireRoles_Allowed(t *testing.T) {
	t.Parallel()

	handler := RequireRoles("admin", "member", "viewer")(http.HandlerFunc(okHandler))

	tests := []struct {
		role string
		want int
	}{
		{RoleAdmin, 200},
		{RoleMember, 200},
		{RoleViewer, 200},
	}
	for _, tt := range tests {
		req := requestWithRole("GET", "/items", tt.role)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tt.want {
			t.Errorf("role %q: expected %d, got %d", tt.role, tt.want, rec.Code)
		}
	}
}

func TestRequireRoles_Denied(t *testing.T) {
	t.Parallel()

	handler := RequireRoles("admin")(http.HandlerFunc(okHandler))

	tests := []struct {
		role string
		want int
	}{
		{RoleMember, 403},
		{RoleViewer, 403},
		{"VideoManager", 403}, // custom role not in allowed list
		{"ADMIN", 403},        // case-sensitive — "ADMIN" ≠ "admin"
		{"", 401},
	}
	for _, tt := range tests {
		req := requestWithRole("GET", "/items", tt.role)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tt.want {
			t.Errorf("role %q: expected %d, got %d", tt.role, tt.want, rec.Code)
		}
	}
}

func TestRequireRoles_AdminOnly(t *testing.T) {
	t.Parallel()

	handler := RequireRoles("admin")(http.HandlerFunc(okHandler))

	req := requestWithRole("POST", "/config", RoleAdmin)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestRequireRoles_ZeroRolesDeniesEverything(t *testing.T) {
	t.Parallel()

	// SECURITY regression guard: an empty roles list must be safe-by-default
	// (deny everyone). A future "simplification" that returned next.ServeHTTP
	// for the zero-roles case would silently open every route guarded by
	// RequireRoles(...) — typically the result of a build-time bug where the
	// roles slice came from configuration that failed to parse.
	handler := RequireRoles()(http.HandlerFunc(okHandler))

	for _, role := range []string{RoleAdmin, RoleMember, RoleViewer, "VideoManager"} {
		req := requestWithRole("GET", "/x", role)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("zero-roles, role=%q: want 403, got %d", role, rec.Code)
		}
	}

	// Anonymous (no AppRole) still 401, not 403, because the
	// authentication-required check fires before the role lookup.
	req := requestWithRole("GET", "/x", "")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("zero-roles, anonymous: want 401, got %d", rec.Code)
	}
}

// Note: registry/manifest tests previously lived here as
// TestRegisteredPermissions. They now live in internal/registry where the
// per-Module Permissions storage actually lives — the auth package no
// longer maintains any cross-call state.
