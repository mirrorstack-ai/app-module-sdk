package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mirrorstack-ai/app-module-sdk/internal/refcache"
)

const (
	defaultMaxPools          = 20
	defaultMaxConnsPerApp    = 2
	defaultIdleTimeout       = 5 * time.Minute
	defaultMaxConnLifetime   = 30 * time.Minute
	defaultHealthCheckPeriod = 30 * time.Second
	defaultResetTimeout      = 2 * time.Second
)

// PoolCache manages per-(host,port,db,user) connection pools. It is a thin
// wrapper around refcache.Cache that adds credential validation, key derivation,
// and pool construction. The refcount + LRU + double-checked-locking lifecycle
// is implemented in refcache.
type PoolCache struct {
	cache *refcache.Cache[*pgxpool.Pool]
}

// NewPoolCache creates a PoolCache with default settings.
func NewPoolCache() *PoolCache {
	return &PoolCache{
		cache: refcache.New[*pgxpool.Pool](defaultMaxPools, "mirrorstack/db: pool", func(p *pgxpool.Pool) {
			p.Close()
		}),
	}
}

// Get returns a pool for the given credential and a release closure. The pool
// is refcount-pinned until release runs, so concurrent eviction cannot close it.
// Pair every Get with a deferred release call.
func (c *PoolCache) Get(ctx context.Context, cred Credential) (*pgxpool.Pool, func(), error) {
	if err := cred.validate(); err != nil {
		return nil, nil, err
	}
	return c.cache.Get(cred.cacheKey(), func() (*pgxpool.Pool, error) {
		return createPool(ctx, cred)
	})
}

// Close closes all pools.
func (c *PoolCache) Close() {
	c.cache.Close()
}

// configurePoolDefaults applies the standard MirrorStack pool settings:
// connection lifetime, idle timeout, health-check period, and the
// AfterRelease scope-cleanup hook. Shared by createPool (per-credential
// production pools) and db.New (single dev pool) so dev mode cannot silently
// drift from prod settings.
func configurePoolDefaults(cfg *pgxpool.Config) {
	cfg.MaxConnIdleTime = defaultIdleTimeout
	cfg.MaxConnLifetime = defaultMaxConnLifetime
	cfg.HealthCheckPeriod = defaultHealthCheckPeriod
	cfg.AfterRelease = afterReleaseReset
}

// createPool builds a pgxpool.Pool from a credential. The token is set
// directly on cfg.ConnConfig.Password instead of being interpolated into a
// DSN string, so a parse error wrapped with %w cannot leak it to logs.
func createPool(ctx context.Context, cred Credential) (*pgxpool.Pool, error) {
	// DSN intentionally excludes the password — any wrapped ParseConfig error
	// would otherwise echo the full connection string into CloudWatch.
	connStr := fmt.Sprintf(
		"host=%s port=%d dbname=%s user=%s sslmode=require",
		cred.Host, cred.Port, cred.Database, cred.Username,
	)
	cfg, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		return nil, fmt.Errorf("mirrorstack/db: invalid credential for user=%s: %w", cred.Username, err)
	}
	cfg.ConnConfig.Password = cred.Token

	cfg.MaxConns = defaultMaxConnsPerApp
	cfg.MinConns = 0
	configurePoolDefaults(cfg)

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("mirrorstack/db: failed to connect to %s:%d: %w", cred.Host, cred.Port, err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("mirrorstack/db: credential rejected by %s:%d: %w", cred.Host, cred.Port, err)
	}
	return pool, nil
}

// afterReleaseReset is the pgxpool.Config.AfterRelease hook. It clears
// search_path and ms.app_id on every connection release before the connection
// goes back to the pool — defense in depth against SDK release paths that
// might skip resetScope (caller panic, missing defer, future bug). If the
// reset fails, the connection is destroyed instead of being reused.
//
// set_config(_, '', false) is used instead of RESET ms.app_id because RESET
// errors out if the custom GUC was never set on this connection (fresh conn
// returning to the pool for the first time).
func afterReleaseReset(conn *pgx.Conn) bool {
	ctx, cancel := context.WithTimeout(context.Background(), defaultResetTimeout)
	defer cancel()
	_, err := conn.Exec(ctx, "RESET search_path; SELECT set_config('ms.app_id', '', false)")
	return err == nil
}

// AcquireScoped acquires a connection from the pool, sets search_path and
// ms.app_id from context via a single batch round trip. The pool's
// AfterRelease hook clears scope on release — no separate reset needed here.
//
// This entry point is for non-transactional access (no surrounding BEGIN), so
// it uses session-scoped SET / set_config(_, _, false). The Tx() function uses
// transaction-local SET LOCAL inside its BEGIN block.
func AcquireScoped(ctx context.Context, pool *pgxpool.Pool) (Querier, func(), error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("mirrorstack/db: failed to acquire connection: %w", err)
	}

	schema := SchemaFrom(ctx)
	if schema != "" {
		if err := applyScope(ctx, conn, schema, false); err != nil {
			conn.Release()
			return nil, nil, err
		}
	}

	return conn, conn.Release, nil
}
