package db

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 🔴 THE POOL MUST NEVER OUTLIVE ITS TOKEN.
//
// The static path baked the FIRST invocation's token into the pool's base
// config. The cache key excludes the token on purpose (rotation must not churn
// pools) and refcache returns a hit without running the factory, so nothing
// could ever replace it. An RDS-IAM token lives ~15 minutes; a warm Lambda
// container lives indefinitely; pooled connections are re-dialled continuously
// (MaxConnIdleTime 5m, MaxConnLifetime 30m, MinConns 0). So every container
// that outlived its first token lost the database permanently.
//
// Measured in production 2026-09-09: user-core answered "failed to acquire
// connection: ... PAM authentication failed (SQLSTATE 28000)" from 09:29Z
// onward — the only one of five deployed modules affected, and the only one
// with a 60-second cron pinning one container warm.
//
// This asserts at the config layer, where the existing renewal tests live, so
// it needs no PostgreSQL: BeforeConnect must hand each NEW connection the token
// current at DIAL time, not the one the pool was built with.
func TestStaticPoolConfigDialsWithTheCurrentEnvelopeToken(t *testing.T) {
	first := Credential{Host: "db.example", Port: 5432, Database: "mirrorstack", Username: "r_app_mod", Token: "token-1"}
	envelope := &envelopeCredential{}
	envelope.set(first)

	cfg, err := createPoolConfig(first, envelope)
	if err != nil {
		t.Fatalf("createPoolConfig: %v", err)
	}
	if cfg.BeforeConnect == nil {
		t.Fatal("no BeforeConnect hook — the pool would dial forever with its build-time token")
	}
	// The base config must not retain the bootstrap token once the hook owns
	// the password; a stale copy there is exactly what this fixes.
	if cfg.ConnConfig.Password != "" {
		t.Fatalf("base config kept a token: %q", cfg.ConnConfig.Password)
	}

	dial := func() string {
		connCfg := cfg.ConnConfig.Copy()
		if err := cfg.BeforeConnect(context.Background(), connCfg); err != nil {
			t.Fatalf("BeforeConnect: %v", err)
		}
		return connCfg.Password
	}

	if got := dial(); got != "token-1" {
		t.Fatalf("first dial password = %q, want token-1", got)
	}

	// The platform mints a new token and the next invocation carries it.
	rotated := first
	rotated.Token = "token-2"
	envelope.set(rotated)
	if got := dial(); got != "token-2" {
		t.Fatalf("dial after rotation = %q, want token-2 — the pool is still using the token it was built with", got)
	}
}

// The scope guard still applies to envelope-fed pools: a credential that moves
// the connection to another host/database/user must be refused rather than
// silently reused on a pool keyed for the old one.
func TestEnvelopePoolRefusesAScopeChange(t *testing.T) {
	first := Credential{Host: "db.example", Port: 5432, Database: "mirrorstack", Username: "r_app_mod", Token: "token-1"}
	envelope := &envelopeCredential{}
	envelope.set(first)
	cfg, err := createPoolConfig(first, envelope)
	if err != nil {
		t.Fatalf("createPoolConfig: %v", err)
	}

	moved := first
	moved.Username = "r_other_mod"
	moved.Token = "token-2"
	envelope.set(moved)

	if err := cfg.BeforeConnect(context.Background(), cfg.ConnConfig.Copy()); err == nil {
		t.Fatal("a scope change was accepted; the pool would authenticate as a different role")
	}
}

// 🔴 THE WIRING, NOT THE HOOK. The two tests above prove BeforeConnect reads
// the current credential — but the production defect was never in the hook. It
// was that the static path built its pool with NO provider at all, and that a
// cache HIT returned the pool without the factory ever running, so a rotated
// token had nowhere to land.
//
// So this asserts what PoolCache.Get actually wires: that a provider reaches
// the pool, and that the SECOND Get — a cache hit — makes that provider yield
// the SECOND token. Both are single lines in Get, and removing either one must
// fail here.
func TestPoolCacheGetWiresAProviderAndRefreshesItOnACacheHit(t *testing.T) {
	var captured CredentialProvider
	builds := 0
	cache := NewPoolCache()
	cache.newPool = func(_ context.Context, _ Credential, provider CredentialProvider) (*pgxpool.Pool, error) {
		builds++
		captured = provider
		return nil, nil
	}

	cred := Credential{Host: "db.example", Port: 5432, Database: "mirrorstack", Username: "r_app_mod", Token: "token-1"}
	if _, release, err := cache.Get(context.Background(), cred); err != nil {
		t.Fatalf("first Get: %v", err)
	} else {
		release()
	}
	if captured == nil {
		t.Fatal("Get built the pool with NO credential provider — every connection would dial with the build-time token forever")
	}

	// The platform rotates the token; the next invocation carries the new one.
	rotated := cred
	rotated.Token = "token-2"
	if _, release, err := cache.Get(context.Background(), rotated); err != nil {
		t.Fatalf("second Get: %v", err)
	} else {
		release()
	}
	if builds != 1 {
		t.Fatalf("pool builds = %d, want 1 — rotation must reuse the pool, not churn it", builds)
	}

	got, err := captured.Credential(context.Background())
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if got.Token != "token-2" {
		t.Fatalf("provider token after a cache hit = %q, want token-2 — the hit did not refresh the credential, so the pool is still dialling with the token it was built with", got.Token)
	}
}
