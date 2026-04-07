package cache

import (
	"context"
	"strings"
	"testing"
)

func TestCredential_Validate(t *testing.T) {
	t.Parallel()

	const secret = "super-secret-token-xyz"
	full := Credential{Endpoint: "redis.example.com:6379", Username: "mod_media", Token: secret}
	if err := full.validate(); err != nil {
		t.Errorf("full credential should validate, got %v", err)
	}

	cases := []struct {
		name string
		cred Credential
	}{
		{"missing endpoint", Credential{Username: "mod_media", Token: secret}},
		{"missing username", Credential{Endpoint: "redis.example.com:6379", Token: secret}},
		{"missing token", Credential{Endpoint: "redis.example.com:6379", Username: "mod_media"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cred.validate()
			if err == nil {
				t.Fatal("expected validation error")
			}
			if tt.cred.Token != "" && strings.Contains(err.Error(), tt.cred.Token) {
				t.Errorf("validation error leaked token: %v", err)
			}
		})
	}
}

func TestCredential_CacheKey_ExcludesToken(t *testing.T) {
	t.Parallel()

	a := Credential{Endpoint: "redis.example.com:6379", Username: "mod_media", Token: "old-token"}
	b := Credential{Endpoint: "redis.example.com:6379", Username: "mod_media", Token: "new-token"}

	if a.cacheKey() != b.cacheKey() {
		t.Errorf("cacheKey should ignore Token (so rotation reuses client): %q vs %q", a.cacheKey(), b.cacheKey())
	}
	if strings.Contains(a.cacheKey(), "old-token") {
		t.Errorf("cacheKey leaked token: %q", a.cacheKey())
	}
}

// TestClientCache_Get_RejectsInvalidCredential verifies the wrapper's validate
// step runs before delegating to refcache. The refcount/eviction/factory logic
// itself is covered by internal/refcache tests.
func TestClientCache_Get_RejectsInvalidCredential(t *testing.T) {
	t.Parallel()

	cache := NewClientCache()
	defer cache.Close()

	_, _, err := cache.Get(context.Background(), Credential{})
	if err == nil {
		t.Error("expected error for empty credential")
	}
}
