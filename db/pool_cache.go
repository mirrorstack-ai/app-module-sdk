package db

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultMaxPools       = 20
	defaultMaxConnsPerApp = 2
	defaultIdleTimeout    = 5 * time.Minute
)

// PoolCache manages per-app connection pools. Each app gets its own pool
// keyed by credential username. LRU eviction at maxPools.
type PoolCache struct {
	mu       sync.Mutex
	pools    map[string]*poolEntry
	maxPools int
}

type poolEntry struct {
	pool     *pgxpool.Pool
	lastUsed time.Time
}

// NewPoolCache creates a PoolCache with default settings.
func NewPoolCache() *PoolCache {
	return &PoolCache{
		pools:    make(map[string]*poolEntry),
		maxPools: defaultMaxPools,
	}
}

// Get returns a pool for the given credential. Creates one if not cached.
func (c *PoolCache) Get(ctx context.Context, cred Credential) (*pgxpool.Pool, error) {
	key := cred.Username

	c.mu.Lock()
	defer c.mu.Unlock()

	if entry, ok := c.pools[key]; ok {
		entry.lastUsed = time.Now()
		return entry.pool, nil
	}

	if len(c.pools) >= c.maxPools {
		c.evictOldest()
	}

	connStr := fmt.Sprintf(
		"host=%s port=%d dbname=%s user=%s password=%s sslmode=require",
		cred.Host, cred.Port, cred.Database, cred.Username, cred.Token,
	)

	cfg, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		return nil, fmt.Errorf("mirrorstack/db: invalid credential: %w", err)
	}
	cfg.MaxConns = defaultMaxConnsPerApp
	cfg.MinConns = 0
	cfg.MaxConnIdleTime = defaultIdleTimeout

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("mirrorstack/db: failed to connect: %w", err)
	}

	c.pools[key] = &poolEntry{pool: pool, lastUsed: time.Now()}
	return pool, nil
}

func (c *PoolCache) evictOldest() {
	var oldestKey string
	var oldestTime time.Time

	for key, entry := range c.pools {
		// Skip pools with active connections
		if entry.pool.Stat().AcquiredConns() > 0 {
			continue
		}
		if oldestKey == "" || entry.lastUsed.Before(oldestTime) {
			oldestKey = key
			oldestTime = entry.lastUsed
		}
	}

	if oldestKey != "" {
		c.pools[oldestKey].pool.Close()
		delete(c.pools, oldestKey)
	}
}

// Close closes all pools.
func (c *PoolCache) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	old := c.pools
	c.pools = make(map[string]*poolEntry)
	for _, entry := range old {
		entry.pool.Close()
	}
}

// AcquireScoped acquires a connection from the pool, sets search_path and
// ms.app_id from context via a single batch round trip. Always resets on release.
func AcquireScoped(ctx context.Context, pool *pgxpool.Pool) (Querier, func(), error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("mirrorstack/db: failed to acquire connection: %w", err)
	}

	schema := SchemaFrom(ctx)
	if schema != "" {
		if err := applyScope(ctx, conn, schema); err != nil {
			conn.Release()
			return nil, nil, err
		}
	}

	release := func() {
		// Always reset — prevents leaking previous tenant's scope
		resetScope(conn)
		conn.Release()
	}

	return conn, release, nil
}
