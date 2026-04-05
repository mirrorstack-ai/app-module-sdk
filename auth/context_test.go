package auth

import (
	"context"
	"testing"
)

func TestUserID(t *testing.T) {
	ctx := context.Background()
	if id := UserID(ctx); id != "" {
		t.Errorf("expected empty, got %q", id)
	}

	ctx = WithUserID(ctx, "user-123")
	if id := UserID(ctx); id != "user-123" {
		t.Errorf("expected 'user-123', got %q", id)
	}
}

func TestAppID(t *testing.T) {
	ctx := context.Background()
	if id := AppID(ctx); id != "" {
		t.Errorf("expected empty, got %q", id)
	}

	ctx = WithAppID(ctx, "app-456")
	if id := AppID(ctx); id != "app-456" {
		t.Errorf("expected 'app-456', got %q", id)
	}
}

func TestAppRole(t *testing.T) {
	ctx := context.Background()
	if role := AppRole(ctx); role != "" {
		t.Errorf("expected empty, got %q", role)
	}

	ctx = WithAppRole(ctx, RoleAdmin)
	if role := AppRole(ctx); role != RoleAdmin {
		t.Errorf("expected %q, got %q", RoleAdmin, role)
	}
}

func TestRoles(t *testing.T) {
	r := Roles("admin", "member")
	if len(r) != 2 || r[0] != "admin" || r[1] != "member" {
		t.Errorf("unexpected: %v", r)
	}
}
