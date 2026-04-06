//go:build integration

package cache

import (
	"context"
	"errors"
	"testing"
	"time"
)

func testClient(t *testing.T) *Client {
	t.Helper()
	c, err := Open(context.Background())
	if err != nil {
		t.Skipf("skipping: cannot connect to Redis: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestIntegration_SetGetRoundtrip(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()

	t.Cleanup(func() { c.Del(ctx, "test:roundtrip") })

	if err := c.Set(ctx, "test:roundtrip", "hello", 1*time.Minute); err != nil {
		t.Fatalf("set: %v", err)
	}

	val, err := c.Get(ctx, "test:roundtrip")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if val != "hello" {
		t.Errorf("expected 'hello', got %q", val)
	}
}

func TestIntegration_Del(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()

	c.Set(ctx, "test:del", "value", 1*time.Minute)

	if err := c.Del(ctx, "test:del"); err != nil {
		t.Fatalf("del: %v", err)
	}

	_, err := c.Get(ctx, "test:del")
	if !errors.Is(err, ErrCacheMiss) {
		t.Errorf("expected ErrCacheMiss after Del, got %v", err)
	}
}

func TestIntegration_GetMissing(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()

	_, err := c.Get(ctx, "test:nonexistent:"+time.Now().String())
	if !errors.Is(err, ErrCacheMiss) {
		t.Errorf("expected ErrCacheMiss, got %v", err)
	}
}

func TestIntegration_KeyPrefixIsolation(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()

	appA := c.ForApp("test_app_a", "mod_media")
	appB := c.ForApp("test_app_b", "mod_media")

	t.Cleanup(func() {
		appA.Del(ctx, "shared_key")
		appB.Del(ctx, "shared_key")
	})

	// Both apps set the same logical key with different values
	appA.Set(ctx, "shared_key", "value_a", 1*time.Minute)
	appB.Set(ctx, "shared_key", "value_b", 1*time.Minute)

	// Each app sees only its own value
	valA, err := appA.Get(ctx, "shared_key")
	if err != nil {
		t.Fatalf("app A get: %v", err)
	}
	if valA != "value_a" {
		t.Errorf("app A: expected 'value_a', got %q", valA)
	}

	valB, err := appB.Get(ctx, "shared_key")
	if err != nil {
		t.Fatalf("app B get: %v", err)
	}
	if valB != "value_b" {
		t.Errorf("app B: expected 'value_b', got %q", valB)
	}

	// Deleting app A's key doesn't affect app B
	appA.Del(ctx, "shared_key")

	_, err = appA.Get(ctx, "shared_key")
	if !errors.Is(err, ErrCacheMiss) {
		t.Errorf("app A: expected ErrCacheMiss after Del, got %v", err)
	}

	valB, err = appB.Get(ctx, "shared_key")
	if err != nil {
		t.Fatalf("app B get after A del: %v", err)
	}
	if valB != "value_b" {
		t.Errorf("app B: expected 'value_b' still, got %q", valB)
	}
}

func TestIntegration_TTLExpires(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()

	c.Set(ctx, "test:ttl", "expires", 1*time.Second)

	val, err := c.Get(ctx, "test:ttl")
	if err != nil {
		t.Fatalf("get before expiry: %v", err)
	}
	if val != "expires" {
		t.Errorf("expected 'expires', got %q", val)
	}

	time.Sleep(1500 * time.Millisecond)

	_, err = c.Get(ctx, "test:ttl")
	if !errors.Is(err, ErrCacheMiss) {
		t.Errorf("expected ErrCacheMiss after TTL, got %v", err)
	}
}
