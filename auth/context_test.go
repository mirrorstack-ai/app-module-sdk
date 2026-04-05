package auth

import (
	"context"
	"testing"
)

func TestGet_NotSet(t *testing.T) {
	if id := Get(context.Background()); id != nil {
		t.Errorf("expected nil, got %+v", id)
	}
}

func TestSetAndGet(t *testing.T) {
	ctx := Set(context.Background(), Identity{
		UserID:  "user-123",
		AppID:   "app-456",
		AppRole: RoleAdmin,
	})

	a := Get(ctx)
	if a == nil {
		t.Fatal("expected identity, got nil")
	}
	if a.UserID != "user-123" {
		t.Errorf("expected UserID 'user-123', got %q", a.UserID)
	}
	if a.AppID != "app-456" {
		t.Errorf("expected AppID 'app-456', got %q", a.AppID)
	}
	if a.AppRole != RoleAdmin {
		t.Errorf("expected AppRole %q, got %q", RoleAdmin, a.AppRole)
	}
}

func TestRoles(t *testing.T) {
	r := Roles("admin", "member")
	if len(r) != 2 || r[0] != "admin" || r[1] != "member" {
		t.Errorf("unexpected: %v", r)
	}
}
