package db

import (
	"context"
	"fmt"
	"os"
	"sync"
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
	cache *refcache.Cache[*pooledDB]
	// newPool builds a pool for a credential and its provider. Production
	// always uses createPoolWithProvider; it is a field only so a test can
	// assert WHICH provider Get wires in, and that a cache hit refreshes it,
	// without a live PostgreSQL. Testing the hook in isolation is not enough:
	// the defect was never in the hook, it was in nothing ever reaching it.
	newPool func(context.Context, Credential, CredentialProvider) (*pgxpool.Pool, error)
}

// envelopeCredential is a CredentialProvider fed by the INVOCATION ENVELOPE
// rather than by a renewal capability.
//
// 🔴 THE POOL OUTLIVES THE TOKEN THAT BUILT IT. Every invocation already
// carries a freshly minted token — the platform mints per (app, role) on a
// ~15-minute TTL and refreshes ahead of expiry — so a module never needs to
// fetch one. It only has to stop pinning the FIRST one. Before this, the static
// path baked that first token into the pool's base config: the cache key
// excludes the token (correct — rotation must not churn pools) and refcache
// returns a hit without running the factory, so nothing could ever replace it.
// Once every pooled connection had cycled (MaxConnIdleTime 5m, MaxConnLifetime
// 30m, MinConns 0) the pool re-dialled with a dead token and the module lost
// its database for the life of the container.
//
// Measured in production 2026-09-09: user-core answered
// `failed to acquire connection: ... PAM authentication failed (SQLSTATE
// 28000)` continuously from 09:29Z. It was the only one of five deployed
// modules affected, and the only one with a 60-second cron holding a single
// container warm — every 28000 came from ONE log stream while a healthy
// module's traffic spanned five. The others went cold and rebuilt their pools
// with fresh tokens, which is what made a platform-wide defect look like one
// module's broken grant.
type envelopeCredential struct {
	mu   sync.RWMutex
	cred Credential
}

func (e *envelopeCredential) set(cred Credential) {
	e.mu.Lock()
	e.cred = cred
	e.mu.Unlock()
}

// Credential implements CredentialProvider by handing back the most recent
// envelope credential. It never fails: the value is always one the caller
// already validated.
func (e *envelopeCredential) Credential(context.Context) (Credential, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cred, nil
}

// pooledDB pairs a pool with the envelope holder its BeforeConnect hook reads,
// so a cache HIT can refresh the credential without rebuilding the pool. The
// holder is nil for pools built from a real renewal provider, which carry
// their own.
type pooledDB struct {
	pool     *pgxpool.Pool
	envelope *envelopeCredential
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
		pool, err := c.newPool(ctx, initial, provider)
		if err != nil {
			return nil, nil, err
		}
		return pool, pool.Close, nil
	}
	entry, release, err := c.cache.Get(initial.cacheKey()+"|renewable|"+keyed.CredentialProviderKey(), func() (*pooledDB, error) {
		pool, err := createPoolWithProvider(ctx, initial, provider)
		if err != nil {
			return nil, err
		}
		return &pooledDB{pool: pool}, nil
	})
	if err != nil {
		return nil, nil, err
	}
	return entry.pool, release, nil
}

// NewPoolCache creates a PoolCache with default settings.
func NewPoolCache() *PoolCache {
	return &PoolCache{
		cache: refcache.New[*pooledDB](defaultMaxPools, "mirrorstack/db: pool", func(p *pooledDB) {
			if p.pool != nil {
				p.pool.Close()
			}
		}),
		newPool: createPoolWithProvider,
	}
}

// Get returns a pool for the given credential and a release closure. The pool
// is refcount-pinned until release runs, so concurrent eviction cannot close it.
// Pair every Get with a deferred release call.
func (c *PoolCache) Get(ctx context.Context, cred Credential) (*pgxpool.Pool, func(), error) {
	if err := cred.validate(); err != nil {
		return nil, nil, err
	}
	entry, release, err := c.cache.Get(cred.cacheKey(), func() (*pooledDB, error) {
		envelope := &envelopeCredential{}
		envelope.set(cred)
		pool, err := c.newPool(ctx, cred, envelope)
		if err != nil {
			return nil, err
		}
		return &pooledDB{pool: pool, envelope: envelope}, nil
	})
	if err != nil {
		return nil, nil, err
	}
	// 🔴 REFRESH ON EVERY GET, HIT OR MISS. refcache's fast path returns the
	// cached value without ever running the factory, so a hit is the ONLY
	// place a rotated token can reach an existing pool.
	if entry.envelope != nil {
		entry.envelope.set(cred)
	}
	return entry.pool, release, nil
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
