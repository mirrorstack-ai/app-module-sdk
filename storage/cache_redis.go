package storage

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisClient is the subset of go-redis we need. Allows mocking.
type RedisClient interface {
	Get(ctx context.Context, key string) *redis.StringCmd
	Set(ctx context.Context, key string, value any, expiration time.Duration) *redis.StatusCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
}

// redisBackend implements cacheBackend using Redis.
type redisBackend struct {
	client RedisClient
}

// NewCacheClient creates a CacheClient backed by Redis.
// Works for both production (ElastiCache) and local dev (local Redis)
// — only the client endpoint differs.
//
//	rdb := redis.NewClient(&redis.Options{Addr: os.Getenv("REDIS_ADDR")})
//	cc := storage.NewCacheClient(rdb, "prod", appID, moduleID)
func NewCacheClient(client RedisClient, stage, appID, moduleID string) *CacheClient {
	return &CacheClient{
		backend: &redisBackend{client: client},
		prefix:  buildCachePrefix(stage, appID, moduleID),
	}
}

// NewLocalCacheClient creates a CacheClient pointing to localhost:6379.
//
//	cc := storage.NewLocalCacheClient("dev", appID, moduleID)
func NewLocalCacheClient(stage, appID, moduleID string) *CacheClient {
	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
	return NewCacheClient(rdb, stage, appID, moduleID)
}

func (b *redisBackend) get(ctx context.Context, key string) (string, error) {
	val, err := b.client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrCacheMiss
	}
	return val, err
}

func (b *redisBackend) set(ctx context.Context, key string, value string, ttl time.Duration) error {
	return b.client.Set(ctx, key, value, ttl).Err()
}

func (b *redisBackend) del(ctx context.Context, key string) error {
	return b.client.Del(ctx, key).Err()
}
