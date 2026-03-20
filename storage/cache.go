package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrCacheMiss is returned when a key does not exist.
var ErrCacheMiss = errors.New("storage: cache miss")

// CacheClient provides scoped cache operations.
// All keys are auto-prefixed:
//
//	mirrorstack-<stage>:applications:<app-id>:<module-id>:<key>
//
// If a Meter is present in the context, auto-tracks cache_ops.
type CacheClient struct {
	backend cacheBackend
	prefix  string
}

// cacheBackend is the internal interface for cache operations.
type cacheBackend interface {
	get(ctx context.Context, key string) (string, error)
	set(ctx context.Context, key string, value string, ttl time.Duration) error
	del(ctx context.Context, key string) error
}

// Get retrieves a value by key. Returns ErrCacheMiss if the key does not exist.
//
//	val, err := cc.Get(ctx, "views:"+videoID)
//	if errors.Is(err, storage.ErrCacheMiss) { ... }
func (cc *CacheClient) Get(ctx context.Context, key string) (string, error) {
	if err := validateKey(key); err != nil {
		return "", err
	}
	val, err := cc.backend.get(ctx, cc.fullKey(key))
	trackOp(ctx, "cache_get")
	return val, err
}

// Set stores a value with a TTL. If ttl is 0, the key does not expire.
//
//	err := cc.Set(ctx, "views:"+videoID, "42", 5*time.Minute)
func (cc *CacheClient) Set(ctx context.Context, key string, value string, ttl time.Duration) error {
	if err := validateKey(key); err != nil {
		return err
	}
	err := cc.backend.set(ctx, cc.fullKey(key), value, ttl)
	trackOp(ctx, "cache_set")
	return err
}

// Del removes a key. No error if the key does not exist.
//
//	err := cc.Del(ctx, "views:"+videoID)
func (cc *CacheClient) Del(ctx context.Context, key string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	err := cc.backend.del(ctx, cc.fullKey(key))
	trackOp(ctx, "cache_del")
	return err
}

// fullKey prepends the scoped prefix to a key.
func (cc *CacheClient) fullKey(key string) string {
	return cc.prefix + key
}

func validateKey(key string) error {
	if key == "" {
		return errors.New("storage: key must not be empty")
	}
	if strings.ContainsRune(key, 0) {
		return errors.New("storage: key must not contain null bytes")
	}
	if strings.HasPrefix(key, ":") {
		return errors.New("storage: key must not start with a colon")
	}
	return nil
}

// buildCachePrefix constructs the standard key prefix.
func buildCachePrefix(stage, appID, moduleID string) string {
	return fmt.Sprintf("mirrorstack-%s:applications:%s:%s:", stage, appID, moduleID)
}
