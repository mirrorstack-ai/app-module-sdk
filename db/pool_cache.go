package db

import (
	"context"
	"fmt"
	"os"
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
	renewableMaxConnLifetime = 10 * time.Minute
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

// GetProvider returns a pool whose BeforeConnect hook asks provider for a
// credential on every new physical connection. The connection scope is pinned
// by the first credential; a provider may rotate only the token/expiry.
func (c *PoolCache) GetProvider(ctx context.Context, provider CredentialProvider) (*pgxpool.Pool, func(), error) {
	if provider == nil {
		return nil, nil, fmt.Errorf("mirrorstack/db: renewable credential provider is missing")
	}
	initial, err := provider.Credential(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("mirrorstack/db: renewable credential unavailable: %w", err)
	}
	if err := initial.validate(); err != nil {
		return nil, nil, err
	}
	keyed, ok := provider.(CredentialProviderKey)
	if !ok || keyed.CredentialProviderKey() == "" {
		pool, err := createPoolWithProvider(ctx, initial, provider)
		if err != nil {
			return nil, nil, err
		}
		return pool, pool.Close, nil
	}
	return c.cache.Get(initial.cacheKey()+"|renewable|"+keyed.CredentialProviderKey(), func() (*pgxpool.Pool, error) {
		return createPoolWithProvider(ctx, initial, provider)
	})
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
// PrepareConn scope-sanitizer hook. Shared by createPool (per-credential
// production pools) and db.New (single dev pool) so dev mode cannot silently
// drift from prod settings.
func configurePoolDefaults(cfg *pgxpool.Config) {
	cfg.MaxConnIdleTime = defaultIdleTimeout
	cfg.MaxConnLifetime = defaultMaxConnLifetime
	cfg.HealthCheckPeriod = defaultHealthCheckPeriod
	// pgx runs AfterRelease asynchronously while the resource still counts as
	// acquired. Keeping cleanup there can exhaust a bounded pool after the SDK
	// has logically released its database scope. Return resources immediately;
	// sanitize them synchronously before the next borrower can observe them.
	cfg.AfterRelease = nil
	cfg.BeforeAcquire = nil
	cfg.PrepareConn = prepareConnReset
}

// createPool builds a pgxpool.Pool from a credential. The token is set
// directly on cfg.ConnConfig.Password instead of being interpolated into a
// DSN string, so a parse error wrapped with %w cannot leak it to logs.
func createPool(ctx context.Context, cred Credential) (*pgxpool.Pool, error) {
	cfg, err := createPoolConfig(cred, nil)
	if err != nil {
		return nil, err
	}
	return connectPool(ctx, cfg, cred)
}

func createPoolWithProvider(ctx context.Context, initial Credential, provider CredentialProvider) (*pgxpool.Pool, error) {
	cfg, err := createPoolConfig(initial, provider)
	if err != nil {
		return nil, err
	}
	return connectPool(ctx, cfg, initial)
}

// createPoolConfig is factored from connection I/O so renewal and scope
// stability can be tested deterministically without a live PostgreSQL server.
func createPoolConfig(cred Credential, provider CredentialProvider) (*pgxpool.Config, error) {
	// DSN intentionally excludes the password — any wrapped ParseConfig error
	// would otherwise echo the full connection string into CloudWatch.
	// sslmode defaults to require (prod: the RDS Proxy endpoint mandates TLS);
	// a local dev-sim Postgres has no TLS, so the dev runner sets
	// MS_MODULE_DB_SSLMODE=disable. Unset always resolves to the secure default.
	sslmode := os.Getenv("MS_MODULE_DB_SSLMODE")
	if sslmode == "" {
		sslmode = "require"
	}
	connStr := fmt.Sprintf(
		"host=%s port=%d dbname=%s user=%s sslmode=%s",
		cred.Host, cred.Port, cred.Database, cred.Username, sslmode,
	)
	cfg, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		return nil, fmt.Errorf("mirrorstack/db: invalid credential for user=%s: %w", cred.Username, err)
	}
	cfg.ConnConfig.Password = cred.Token

	cfg.MaxConns = defaultMaxConnsPerApp
	cfg.MinConns = 0
	configurePoolDefaults(cfg)
	if provider != nil {
		cfg.MaxConnLifetime = renewableMaxConnLifetime
		cfg.BeforeConnect = func(ctx context.Context, connCfg *pgx.ConnConfig) error {
			current, err := provider.Credential(ctx)
			if err != nil {
				return fmt.Errorf("mirrorstack/db: renewable credential unavailable: %w", err)
			}
			if err := current.validate(); err != nil {
				return err
			}
			if current.Host != cred.Host || current.Port != cred.Port || current.Database != cred.Database || current.Username != cred.Username {
				return fmt.Errorf("mirrorstack/db: renewable credential changed connection scope")
			}
			connCfg.Password = current.Token
			return nil
		}
		// The provider supplies every physical connection password. Do not keep
		// the bootstrap token in pgxpool's long-lived base config after installing
		// that hook; each connection copy is populated immediately before dial.
		cfg.ConnConfig.Password = ""
	}
	return cfg, nil
}

func connectPool(ctx context.Context, cfg *pgxpool.Config, cred Credential) (*pgxpool.Pool, error) {
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

// prepareConnReset is the pgxpool.Config.PrepareConn hook. It clears
// search_path and ms.app_id after pool acquisition but before the connection
// reaches any borrower. Release therefore returns the resource synchronously,
// while cross-borrow isolation still fails closed.
//
// A reset failure returns false so pgx destroys the connection and retries a
// different resource. Caller cancellation is returned explicitly instead of
// being hidden behind pgx's bounded retry error.
//
// set_config with an empty-string value is used instead of RESET ms.app_id
// because RESET errors out if the custom GUC was never set on this connection
// (fresh conn entering the pool for the first time).
func prepareConnReset(ctx context.Context, conn *pgx.Conn) (bool, error) {
	resetCtx, cancel := context.WithTimeout(ctx, defaultResetTimeout)
	defer cancel()
	_, err := conn.Exec(resetCtx, "RESET search_path; SELECT set_config('ms.app_id', '', false)")
	if err == nil {
		return true, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, ctxErr
	}
	return false, nil
}

// AcquireScoped acquires a connection from the pool, sets search_path and
// ms.app_id from context via a single batch round trip. The pool's PrepareConn
// hook cleared the previous borrower's scope before Acquire returned.
//
// This entry point is for non-transactional access (no surrounding BEGIN), so
// it uses session-scoped SET / set_config(_, _, false). The Tx() function uses
// transaction-local SET LOCAL inside its BEGIN block.
func AcquireScoped(ctx context.Context, pool *pgxpool.Pool) (Querier, func(), error) {
	return AcquireScopedConn(ctx, pool)
}

// AcquireScopedConn is AcquireScoped but returns the concrete *pgxpool.Conn
// instead of the narrower Querier interface. Callers that need the raw driver
// connection underneath — e.g. COPY FROM STDIN via conn.Conn().PgConn(),
// which db.Querier does not expose — use this; everything else should keep
// using AcquireScoped.
func AcquireScopedConn(ctx context.Context, pool *pgxpool.Pool) (*pgxpool.Conn, func(), error) {
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
