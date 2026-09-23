package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/cache"
	"github.com/mirrorstack-ai/app-module-sdk/dataimport"
	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/actor"
	"github.com/mirrorstack-ai/app-module-sdk/storage"
)

func TestInjectResources_FullInjection(t *testing.T) {
	t.Parallel()

	ctx, err := InjectResources(context.Background(), InjectParams{
		Resources: &Resources{
			DB:      &db.Credential{Host: "h", Port: 5432, Database: "d", Username: "u", Token: "t"},
			Cache:   &cache.Credential{Endpoint: "localhost:6379", Username: "cu"},
			Storage: &storage.Credential{Bucket: "b", Region: "r"},
		},
		UserID:          "user-1",
		AppID:           "app-1",
		AppRole:         "admin",
		AppSchema:       "app_abc123",
		ActorDelegation: "msa1.payload.signature",
	})
	if err != nil {
		t.Fatalf("InjectResources: %v", err)
	}

	if cred := db.CredentialFrom(ctx); cred == nil || cred.Username != "u" {
		t.Errorf("DB credential not injected correctly")
	}
	if cred := cache.CredentialFrom(ctx); cred == nil || cred.Username != "cu" {
		t.Errorf("Cache credential not injected correctly")
	}
	if cred := storage.CredentialFrom(ctx); cred == nil || cred.Bucket != "b" {
		t.Errorf("Storage credential not injected correctly")
	}
	if schema := db.SchemaFrom(ctx); schema != "app_abc123" {
		t.Errorf("schema = %q, want app_abc123", schema)
	}
	if a := auth.Get(ctx); a == nil || a.UserID != "user-1" || a.AppRole != "admin" {
		t.Errorf("auth identity not injected correctly: %+v", a)
	}
	if got := actor.Delegation(ctx); got != "" {
		t.Errorf("actor delegation = %q, want pending until PlatformAuth", got)
	}
}

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

func TestInjectResources_InvalidActorDelegation(t *testing.T) {
	t.Parallel()

	_, err := InjectResources(context.Background(), InjectParams{
		ActorDelegation: "contains whitespace",
	})
	if err == nil {
		t.Fatal("expected invalid actor delegation to be rejected")
	}
	if strings.Contains(err.Error(), "contains whitespace") {
		t.Errorf("error leaked actor delegation: %q", err)
	}
}

func TestInjectResources_Import(t *testing.T) {
	t.Parallel()

	ctx, err := InjectResources(context.Background(), InjectParams{Import: &dataimport.Mode{RunID: "run-1", DryRun: true}})
	if err != nil {
		t.Fatalf("InjectResources: %v", err)
	}
	if m, ok := dataimport.From(ctx); !ok || m.RunID != "run-1" || !m.DryRun {
		t.Fatalf("import mode = %+v, %v", m, ok)
	}

	plain, err := InjectResources(context.Background(), InjectParams{})
	if err != nil {
		t.Fatalf("InjectResources: %v", err)
	}
	if dataimport.Active(plain) {
		t.Fatal("a request without Import must not be in import mode")
	}

	if _, err := InjectResources(context.Background(), InjectParams{Import: &dataimport.Mode{RunID: "bad id"}}); err == nil {
		t.Fatal("a malformed import run id must be rejected")
	}
}
