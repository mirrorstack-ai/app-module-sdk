package mirrorstack_test

import (
	"context"
	"testing"

	ms "github.com/mirrorstack-ai/app-module-sdk"
	"github.com/mirrorstack-ai/app-module-sdk/auth"
)

func TestWithAppID(t *testing.T) {
	t.Run("sets app id on a bare context", func(t *testing.T) {
		ctx := ms.WithAppID(context.Background(), "app-1")
		got := auth.Get(ctx)
		if got == nil {
			t.Fatal("expected identity, got nil")
		}
		if got.AppID != "app-1" {
			t.Errorf("AppID = %q, want app-1", got.AppID)
		}
		if got.UserID != "" || got.AppRole != "" {
			t.Errorf("expected empty UserID/AppRole, got %q/%q", got.UserID, got.AppRole)
		}
	})

	t.Run("overrides app id but preserves user and role", func(t *testing.T) {
		base := auth.Set(context.Background(), auth.Identity{
			UserID:  "u-1",
			AppID:   "app-old",
			AppRole: auth.RoleAdmin,
		})
		ctx := ms.WithAppID(base, "app-new")
		got := auth.Get(ctx)
		if got.AppID != "app-new" {
			t.Errorf("AppID = %q, want app-new", got.AppID)
		}
		if got.UserID != "u-1" || got.AppRole != auth.RoleAdmin {
			t.Errorf("UserID/AppRole not preserved: %q/%q", got.UserID, got.AppRole)
		}
	})

	t.Run("does not mutate the source identity", func(t *testing.T) {
		base := auth.Set(context.Background(), auth.Identity{AppID: "app-old"})
		_ = ms.WithAppID(base, "app-new")
		if src := auth.Get(base); src.AppID != "app-old" {
			t.Errorf("source identity mutated: AppID = %q, want app-old", src.AppID)
		}
	})
}

func TestAppID(t *testing.T) {
	t.Run("returns empty string when no identity is set", func(t *testing.T) {
		if got := ms.AppID(context.Background()); got != "" {
			t.Errorf("AppID = %q, want empty string", got)
		}
	})

	t.Run("reads the app id from the context identity", func(t *testing.T) {
		ctx := auth.Set(context.Background(), auth.Identity{
			UserID:  "u-1",
			AppID:   "app-7",
			AppRole: auth.RoleAdmin,
		})
		if got := ms.AppID(ctx); got != "app-7" {
			t.Errorf("AppID = %q, want app-7", got)
		}
	})

	t.Run("is the inbound twin of WithAppID", func(t *testing.T) {
		ctx := ms.WithAppID(context.Background(), "app-rt")
		if got := ms.AppID(ctx); got != "app-rt" {
			t.Errorf("AppID = %q, want app-rt", got)
		}
	})
}
