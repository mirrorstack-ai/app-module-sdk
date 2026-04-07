package cache

import (
	"context"

	"github.com/mirrorstack-ai/app-module-sdk/internal/refcache"
)

const defaultMaxClients = 100

// ClientCache manages per-(endpoint, username) Redis clients. It is a thin
// wrapper around refcache.Cache that adds credential validation, key derivation,
// and client construction. The refcount + LRU + double-checked-locking lifecycle
// is implemented in refcache.
type ClientCache struct {
	cache *refcache.Cache[*Client]
}

// NewClientCache creates a ClientCache with default settings.
func NewClientCache() *ClientCache {
	return &ClientCache{
		cache: refcache.New[*Client](defaultMaxClients, "mirrorstack/cache: client", func(c *Client) {
			_ = c.Close()
		}),
	}
}

// Get returns a client for the given credential plus a release closure. The
// client is refcount-pinned until release runs, so concurrent eviction cannot
// close it. Pair every Get with a deferred release call.
func (c *ClientCache) Get(ctx context.Context, cred Credential) (*Client, func(), error) {
	if err := cred.validate(); err != nil {
		return nil, nil, err
	}
	return c.cache.Get(cred.cacheKey(), func() (*Client, error) {
		return NewFromCredential(ctx, cred)
	})
}

// Close closes all cached clients.
func (c *ClientCache) Close() {
	c.cache.Close()
}
