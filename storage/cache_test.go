package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mirrorstack-ai/app-module-sdk/storage"
	"github.com/redis/go-redis/v9"
)

// --- Mock Redis ---

type mockRedis struct {
	data   map[string]string
	gotKey string
}

func newMockRedis() *mockRedis {
	return &mockRedis{data: make(map[string]string)}
}

func (m *mockRedis) Get(_ context.Context, key string) *redis.StringCmd {
	m.gotKey = key
	val, ok := m.data[key]
	if !ok {
		cmd := redis.NewStringCmd(context.Background())
		cmd.SetErr(redis.Nil)
		return cmd
	}
	cmd := redis.NewStringCmd(context.Background())
	cmd.SetVal(val)
	return cmd
}

func (m *mockRedis) Set(_ context.Context, key string, value any, _ time.Duration) *redis.StatusCmd {
	m.gotKey = key
	m.data[key] = value.(string)
	cmd := redis.NewStatusCmd(context.Background())
	cmd.SetVal("OK")
	return cmd
}

func (m *mockRedis) Del(_ context.Context, keys ...string) *redis.IntCmd {
	for _, k := range keys {
		m.gotKey = k
		delete(m.data, k)
	}
	cmd := redis.NewIntCmd(context.Background())
	cmd.SetVal(int64(len(keys)))
	return cmd
}

// --- Key prefix ---

func TestCacheClient_FullKey(t *testing.T) {
	mock := newMockRedis()
	cc := storage.NewCacheClient(mock, "prod", "app-x7k2", "video")

	cc.Set(context.Background(), "views:v-123", "42", time.Minute)

	want := "mirrorstack-prod:applications:app-x7k2:video:views:v-123"
	if mock.gotKey != want {
		t.Errorf("key: got %q, want %q", mock.gotKey, want)
	}
}

func TestCacheClient_DevPrefix(t *testing.T) {
	mock := newMockRedis()
	cc := storage.NewCacheClient(mock, "dev", "app-local", "credit")

	cc.Set(context.Background(), "balance:u-1", "100", 0)

	want := "mirrorstack-dev:applications:app-local:credit:balance:u-1"
	if mock.gotKey != want {
		t.Errorf("key: got %q, want %q", mock.gotKey, want)
	}
}

// --- Get ---

func TestCacheClient_Get_Hit(t *testing.T) {
	mock := newMockRedis()
	cc := storage.NewCacheClient(mock, "prod", "app-1", "video")

	fullKey := "mirrorstack-prod:applications:app-1:video:views:v-1"
	mock.data[fullKey] = "42"

	val, err := cc.Get(context.Background(), "views:v-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "42" {
		t.Errorf("value: got %q, want %q", val, "42")
	}
}

func TestCacheClient_Get_Miss(t *testing.T) {
	mock := newMockRedis()
	cc := storage.NewCacheClient(mock, "prod", "app-1", "video")

	_, err := cc.Get(context.Background(), "nonexistent")
	if !errors.Is(err, storage.ErrCacheMiss) {
		t.Errorf("expected ErrCacheMiss, got %v", err)
	}
}

// --- Set ---

func TestCacheClient_Set(t *testing.T) {
	mock := newMockRedis()
	cc := storage.NewCacheClient(mock, "prod", "app-1", "video")

	err := cc.Set(context.Background(), "views:v-1", "42", 5*time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fullKey := "mirrorstack-prod:applications:app-1:video:views:v-1"
	if mock.data[fullKey] != "42" {
		t.Errorf("stored value: got %q, want %q", mock.data[fullKey], "42")
	}
}

func TestCacheClient_Set_ZeroTTL(t *testing.T) {
	mock := newMockRedis()
	cc := storage.NewCacheClient(mock, "prod", "app-1", "video")

	err := cc.Set(context.Background(), "permanent", "value", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- Del ---

func TestCacheClient_Del(t *testing.T) {
	mock := newMockRedis()
	cc := storage.NewCacheClient(mock, "prod", "app-1", "video")

	fullKey := "mirrorstack-prod:applications:app-1:video:views:v-1"
	mock.data[fullKey] = "42"

	err := cc.Del(context.Background(), "views:v-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, exists := mock.data[fullKey]; exists {
		t.Error("key should have been deleted")
	}
}

// --- Key validation ---

func TestCacheClient_EmptyKey_ReturnsError(t *testing.T) {
	mock := newMockRedis()
	cc := storage.NewCacheClient(mock, "prod", "app-1", "video")

	_, err := cc.Get(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty key")
	}

	err = cc.Set(context.Background(), "", "val", time.Minute)
	if err == nil {
		t.Fatal("expected error for empty key")
	}

	err = cc.Del(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty key")
	}
}

func TestCacheClient_LeadingColon_ReturnsError(t *testing.T) {
	mock := newMockRedis()
	cc := storage.NewCacheClient(mock, "prod", "app-1", "video")

	_, err := cc.Get(context.Background(), ":applications:other-app:video:secret")
	if err == nil {
		t.Fatal("expected error for leading colon key")
	}
}

func TestCacheClient_NullByte_ReturnsError(t *testing.T) {
	mock := newMockRedis()
	cc := storage.NewCacheClient(mock, "prod", "app-1", "video")

	_, err := cc.Get(context.Background(), "key\x00injected")
	if err == nil {
		t.Fatal("expected error for null byte in key")
	}
}

func TestCacheClient_ColonInMiddle_Allowed(t *testing.T) {
	mock := newMockRedis()
	cc := storage.NewCacheClient(mock, "prod", "app-1", "video")

	// Colons in the middle are fine (views:v-123 is idiomatic).
	err := cc.Set(context.Background(), "views:v-123", "42", time.Minute)
	if err != nil {
		t.Fatalf("colons in middle should be allowed: %v", err)
	}
}
