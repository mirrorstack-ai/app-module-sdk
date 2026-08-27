// Package cache provides scoped Redis caching with per-app key prefix isolation.
//
// Production: credentials injected per invocation via Lambda payload.
// Dev: REDIS_URL env var with localhost fallback.
package cache

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/mirrorstack-ai/app-module-sdk/internal/lambdaenv"
)

// idPattern guards the key prefix, and the ONLY thing it has to guard against
// is a colon: keys are built as {appID}:{moduleID}:{key}, so an id containing
// ':' could climb out of its own namespace and read another app's data.
//
// 🔴 IT USED TO EXCLUDE HYPHENS, WHICH MADE IT PANIC ON EVERY REAL APP ID.
// App ids are UUIDs — `bb8a3f8b-1234-…` — and runtime.AppSchemaName exists
// precisely because of it, rewriting '-' to '_' before the value can be used as
// a Postgres identifier. Nothing did the equivalent here, so the first module
// to reach for ms.Cache().ForApp(ms.AppID(ctx), …) crashed on its first real
// request. The unit test passed because it used a hand-written "app_abc123".
//
// A hyphen cannot break a prefix boundary. Excluding it bought nothing and
// cost the whole cache.
var idPattern = regexp.MustCompile(`^[a-z0-9_-]+$`)

const defaultDevURL = "redis://localhost:6379"

// ErrCacheMiss is returned by Get when the key does not exist.
var ErrCacheMiss = errors.New("mirrorstack/cache: key not found")

// Cacher is the interface module developers see when they call Module.Cache(ctx).
// Close is intentionally NOT exposed: the underlying *Client is owned by the SDK's
// ClientCache and must not be closed by handler code.
type Cacher interface {
	Set(ctx context.Context, key, value string, ttl time.Duration) error
	Get(ctx context.Context, key string) (string, error)
	Del(ctx context.Context, key string) error
}

// Client wraps a go-redis client with app-scoped key prefixing.
type Client struct {
	rdb    *redis.Client
	prefix string // e.g., "app_abc123:mod_media:"
}

// Open creates a Client from REDIS_URL env var, falling back to localhost.
// Cannot be used in Lambda — credentials are injected per invocation.
func Open(ctx context.Context) (*Client, error) {
	if lambdaenv.IsSet() {
		return nil, fmt.Errorf("mirrorstack/cache: Open() cannot be used in Lambda — credentials are injected per-invocation")
	}
	url := os.Getenv("REDIS_URL")
	if url == "" {
		url = defaultDevURL
	}
	return New(ctx, url)
}

// New creates a Client from a Redis URL string.
func New(ctx context.Context, redisURL string) (*Client, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("mirrorstack/cache: invalid Redis URL: %w", err)
	}
	rdb := redis.NewClient(opts)
	if err := rdb.Ping(ctx).Err(); err != nil {
		rdb.Close()
		return nil, fmt.Errorf("mirrorstack/cache: failed to ping Redis: %w", err)
	}
	return &Client{rdb: rdb}, nil
}

// NewFromCredential creates a Client from platform-injected credentials.
// Enforces TLS for production ElastiCache connections.
func NewFromCredential(ctx context.Context, cred Credential) (*Client, error) {
	if err := cred.validate(); err != nil {
		return nil, err
	}
	opts := &redis.Options{
		Addr:      cred.Endpoint,
		Username:  cred.Username,
		Password:  cred.Token,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}
	rdb := redis.NewClient(opts)
	if err := rdb.Ping(ctx).Err(); err != nil {
		rdb.Close()
		return nil, fmt.Errorf("mirrorstack/cache: credential rejected by Redis: %w", err)
	}
	return &Client{rdb: rdb}, nil
}

// NewFromProvider creates a Redis client that reacquires credentials whenever
// go-redis authenticates a new connection. Endpoint/username are pinned to the
// first credential so renewal cannot silently cross a resource boundary.
func NewFromProvider(ctx context.Context, provider CredentialProvider) (*Client, error) {
	opts, err := optionsFromProvider(ctx, provider)
	if err != nil {
		return nil, err
	}
	rdb := redis.NewClient(opts)
	if err := rdb.Ping(ctx).Err(); err != nil {
		rdb.Close()
		return nil, fmt.Errorf("mirrorstack/cache: credential rejected by Redis: %w", err)
	}
	return &Client{rdb: rdb}, nil
}

func optionsFromProvider(ctx context.Context, provider CredentialProvider) (*redis.Options, error) {
	if provider == nil {
		return nil, fmt.Errorf("mirrorstack/cache: renewable credential provider is missing")
	}
	initial, err := provider.Credential(ctx)
	if err != nil {
		return nil, fmt.Errorf("mirrorstack/cache: renewable credential unavailable: %w", err)
	}
	if err := initial.validate(); err != nil {
		return nil, err
	}
	// Retain only immutable routing coordinates. Keeping the full initial
	// credential in this closure would keep its short-lived token reachable for
	// the lifetime of the Redis client even after the provider rotates it.
	initialEndpoint := initial.Endpoint
	initialUsername := initial.Username
	initial = Credential{}
	opts := &redis.Options{
		Addr:      initialEndpoint,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		CredentialsProviderContext: func(ctx context.Context) (string, string, error) {
			current, err := provider.Credential(ctx)
			if err != nil {
				return "", "", fmt.Errorf("mirrorstack/cache: renewable credential unavailable: %w", err)
			}
			if err := current.validate(); err != nil {
				return "", "", err
			}
			if current.Endpoint != initialEndpoint || current.Username != initialUsername {
				return "", "", fmt.Errorf("mirrorstack/cache: renewable credential changed connection scope")
			}
			return current.Username, current.Token, nil
		},
	}
	return opts, nil
}

// ForApp returns a new Client sharing the same Redis connection but with an
// app-scoped key prefix. Key pattern: {appID}:{moduleID}:{key}
// Validates both IDs to prevent colon injection breaking prefix boundary.
func (c *Client) ForApp(appID, moduleID string) *Client {
	if appID != "" && !idPattern.MatchString(appID) {
		panic(fmt.Sprintf("mirrorstack/cache: invalid appID %q — must match %s", appID, idPattern))
	}
	if !idPattern.MatchString(moduleID) {
		panic(fmt.Sprintf("mirrorstack/cache: invalid moduleID %q — must match %s", moduleID, idPattern))
	}
	return &Client{
		rdb:    c.rdb,
		prefix: appID + ":" + moduleID + ":",
	}
}

// Set stores a value with the given TTL. TTL of 0 means no expiration.
func (c *Client) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	return c.rdb.Set(ctx, c.prefix+key, value, ttl).Err()
}

// Get retrieves a value. Returns ErrCacheMiss if the key does not exist.
func (c *Client) Get(ctx context.Context, key string) (string, error) {
	val, err := c.rdb.Get(ctx, c.prefix+key).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrCacheMiss
	}
	if err != nil {
		return "", fmt.Errorf("mirrorstack/cache: get failed: %w", err)
	}
	return val, nil
}

// Del removes a key.
func (c *Client) Del(ctx context.Context, key string) error {
	return c.rdb.Del(ctx, c.prefix+key).Err()
}

// Close closes the Redis connection.
func (c *Client) Close() error {
	if c.rdb != nil {
		return c.rdb.Close()
	}
	return nil
}
