package runtime

import (
	"context"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/db"
)

func TestInjectResources_EmptyParams(t *testing.T) {
	t.Parallel()

	ctx, err := InjectResources(context.Background(), InjectParams{})
	if err != nil {
		t.Fatalf("InjectResources with empty params: %v", err)
	}
	if db.CredentialFrom(ctx) != nil {
		t.Error("expected nil DB credential for empty params")
	}
}

func TestInjectResources_InvalidSchema(t *testing.T) {
	t.Parallel()

	_, err := InjectResources(context.Background(), InjectParams{
		AppSchema: `app"; DROP TABLE users;--`,
	})
	if err == nil {
		t.Error("expected error for invalid schema")
	}
}

func TestInjectResources_InvalidRole(t *testing.T) {
	t.Parallel()

	_, err := InjectResources(context.Background(), InjectParams{
		AppRole: "superadmin",
	})
	if err == nil {
		t.Error("expected error for unknown role")
	}
}

func TestInjectResources_EmptyRoleAllowed(t *testing.T) {
	t.Parallel()

	_, err := InjectResources(context.Background(), InjectParams{
		AppRole: "",
	})
	if err != nil {
		t.Errorf("empty role should be allowed: %v", err)
	}
}
