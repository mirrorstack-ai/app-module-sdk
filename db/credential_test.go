package db

import (
	"context"
	"testing"
)

func TestWithCredential_RoundTrip(t *testing.T) {
	t.Parallel()

	cred := Credential{Host: "h", Port: 5432, Database: "d", Username: "app_user", Token: "tok"}
	ctx := WithCredential(context.Background(), cred)

	got := CredentialFrom(ctx)
	if got == nil {
		t.Fatal("CredentialFrom returned nil")
	}
	if got.Username != "app_user" {
		t.Errorf("Username = %q, want app_user", got.Username)
	}

	// ModuleCredentialFrom on the same context must NOT see the app credential.
	if mod := ModuleCredentialFrom(ctx); mod != nil {
		t.Errorf("ModuleCredentialFrom returned %+v on app-only context, want nil", mod)
	}
}

func TestWithModuleCredential_RoundTrip(t *testing.T) {
	t.Parallel()

	cred := Credential{Host: "h", Port: 5432, Database: "d", Username: "mod_user", Token: "tok"}
	ctx := WithModuleCredential(context.Background(), cred)

	got := ModuleCredentialFrom(ctx)
	if got == nil {
		t.Fatal("ModuleCredentialFrom returned nil")
	}
	if got.Username != "mod_user" {
		t.Errorf("Username = %q, want mod_user", got.Username)
	}

	// CredentialFrom on the same context must NOT see the module credential.
	if app := CredentialFrom(ctx); app != nil {
		t.Errorf("CredentialFrom returned %+v on module-only context, want nil", app)
	}
}

func TestWithCredential_BothScopesCoexist(t *testing.T) {
	t.Parallel()

	// A handler that needs both app and module data gets both credentials
	// in its context. The two context keys are independent — setting one
	// must not overwrite the other.
	appCred := Credential{Host: "h", Port: 5432, Database: "d", Username: "app_user", Token: "atok"}
	modCred := Credential{Host: "h", Port: 5432, Database: "d", Username: "mod_user", Token: "mtok"}

	ctx := WithCredential(context.Background(), appCred)
	ctx = WithModuleCredential(ctx, modCred)

	gotApp := CredentialFrom(ctx)
	if gotApp == nil || gotApp.Username != "app_user" {
		t.Errorf("CredentialFrom = %+v, want app_user", gotApp)
	}
	gotMod := ModuleCredentialFrom(ctx)
	if gotMod == nil || gotMod.Username != "mod_user" {
		t.Errorf("ModuleCredentialFrom = %+v, want mod_user", gotMod)
	}
}

func TestModuleCredentialFrom_NoCredential(t *testing.T) {
	t.Parallel()

	if got := ModuleCredentialFrom(context.Background()); got != nil {
		t.Errorf("ModuleCredentialFrom on bare context = %+v, want nil", got)
	}
}
