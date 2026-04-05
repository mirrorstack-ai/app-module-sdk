package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequirePermission_Allowed(t *testing.T) {
	t.Cleanup(ResetPermissions)

	handler := RequirePermission("media.view", "admin", "member", "viewer")(http.HandlerFunc(okHandler))

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

func TestRequirePermission_Denied(t *testing.T) {
	t.Cleanup(ResetPermissions)

	handler := RequirePermission("media.delete", "admin")(http.HandlerFunc(okHandler))

	tests := []struct {
		role string
		want int
	}{
		{RoleMember, 403},
		{RoleViewer, 403},
		{"VideoManager", 403},  // custom role not in allowed list
		{"ADMIN", 403},         // case-sensitive — "ADMIN" ≠ "admin"
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

func TestRequirePermission_AdminOnly(t *testing.T) {
	t.Cleanup(ResetPermissions)

	handler := RequirePermission("media.config", "admin")(http.HandlerFunc(okHandler))

	req := requestWithRole("POST", "/config", RoleAdmin)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestRegisteredPermissions(t *testing.T) {
	t.Cleanup(ResetPermissions)

	RequirePermission("media.view", "admin", "member", "viewer")
	RequirePermission("media.upload", "admin", "member")
	RequirePermission("media.delete", "admin")
	// Duplicate should be skipped
	RequirePermission("media.view", "admin", "member", "viewer")

	perms := RegisteredPermissions()
	if len(perms) != 3 {
		t.Fatalf("expected 3 permissions, got %d", len(perms))
	}

	expected := map[string]int{
		"media.view":   3,
		"media.upload": 2,
		"media.delete": 1,
	}
	for _, p := range perms {
		wantRoles, ok := expected[p.Name]
		if !ok {
			t.Errorf("unexpected permission: %s", p.Name)
		}
		if len(p.Roles) != wantRoles {
			t.Errorf("%s: expected %d roles, got %d", p.Name, wantRoles, len(p.Roles))
		}
	}
}
