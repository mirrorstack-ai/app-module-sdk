package cache

import (
	"context"
	"fmt"

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

// GetProvider returns a client backed by renewable authentication.
func (c *ClientCache) GetProvider(ctx context.Context, provider CredentialProvider) (*Client, func(), error) {
	if provider == nil {
		return nil, nil, fmt.Errorf("mirrorstack/cache: renewable credential provider is missing")
	}
	initial, err := provider.Credential(ctx)
	if err != nil {
		return nil, nil, err
	}
	if err := initial.validate(); err != nil {
		return nil, nil, err
	}
	keyed, ok := provider.(CredentialProviderKey)
	if !ok || keyed.CredentialProviderKey() == "" {
		client, err := NewFromProvider(ctx, provider)
		if err != nil {
			return nil, nil, err
		}
		return client, func() { _ = client.Close() }, nil
	}
	return c.cache.Get(initial.cacheKey()+"|renewable|"+keyed.CredentialProviderKey(), func() (*Client, error) {
		return NewFromProvider(ctx, provider)
	})
}

// Close closes all cached clients.
func (c *ClientCache) Close() {
	c.cache.Close()
}
