package db

import (
	"context"
	"strings"
	"testing"
)

func TestWithSchema(t *testing.T) {
	ctx := context.Background()

	if s := SchemaFrom(ctx); s != "" {
		t.Errorf("expected empty schema, got %q", s)
	}

	ctx = WithSchema(ctx, "app_abc123")
	if s := SchemaFrom(ctx); s != "app_abc123" {
		t.Errorf("expected 'app_abc123', got %q", s)
	}
}

func TestNew_InvalidURL(t *testing.T) {
	_, err := New(context.Background(), "not-a-url")
	if err == nil {
		t.Error("expected error for invalid connection string")
	}
}

func TestCredential_Validate(t *testing.T) {
	t.Parallel()

	const secret = "super-secret-token-xyz"
	full := Credential{Host: "h", Port: 5432, Database: "d", Username: "u", Token: secret}
	if err := full.validate(); err != nil {
		t.Errorf("full credential should validate, got %v", err)
	}

	cases := []struct {
		name string
		cred Credential
	}{
		{"missing host", Credential{Port: 5432, Database: "d", Username: "u", Token: secret}},
		{"missing port", Credential{Host: "h", Database: "d", Username: "u", Token: secret}},
		{"missing database", Credential{Host: "h", Port: 5432, Username: "u", Token: secret}},
		{"missing username", Credential{Host: "h", Port: 5432, Database: "d", Token: secret}},
		{"missing token", Credential{Host: "h", Port: 5432, Database: "d", Username: "u"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cred.validate()
			if err == nil {
				t.Fatal("expected validation error")
			}
			// Critical: error must never echo the token.
			if tt.cred.Token != "" && strings.Contains(err.Error(), tt.cred.Token) {
				t.Errorf("validation error leaked token: %v", err)
			}
		})
	}
}

func TestCredential_CacheKey_ExcludesToken(t *testing.T) {
	t.Parallel()

	a := Credential{Host: "h", Port: 5432, Database: "d", Username: "u", Token: "old-token"}
	b := Credential{Host: "h", Port: 5432, Database: "d", Username: "u", Token: "new-token"}

	if a.cacheKey() != b.cacheKey() {
		t.Errorf("cacheKey should ignore Token (so rotation reuses pool): %q vs %q", a.cacheKey(), b.cacheKey())
	}
	if strings.Contains(a.cacheKey(), "old-token") {
		t.Errorf("cacheKey leaked token: %q", a.cacheKey())
	}
}

// TestPoolCache_Get_RejectsInvalidCredential verifies the wrapper's validate
// step runs before delegating to refcache. The refcount/eviction/factory logic
// itself is covered by internal/refcache tests.
func TestPoolCache_Get_RejectsInvalidCredential(t *testing.T) {
	t.Parallel()

	cache := NewPoolCache()
	defer cache.Close()

	_, _, err := cache.Get(context.Background(), Credential{})
	if err == nil {
		t.Error("expected error for empty credential")
	}
}
