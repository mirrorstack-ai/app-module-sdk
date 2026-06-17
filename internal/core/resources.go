package core

import (
	"context"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/cache"
	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
	"github.com/mirrorstack-ai/app-module-sdk/meter"
	"github.com/mirrorstack-ai/app-module-sdk/storage"
)

// Cache returns a scoped cache client. Keys are auto-prefixed with {appID}:{moduleID}:.
//
//	c, release, err := mod.Cache(r.Context())
//	if err != nil { ... }
//	defer release()
//	c.Set(ctx, "views:123", "42", 5*time.Minute)
//	val, err := c.Get(ctx, "views:123")
func (m *Module) Cache(ctx context.Context) (cache.Cacher, func(), error) {
	client, releaseClient, err := m.resolveCache(ctx)
	if err != nil {
		return nil, nil, err
	}
	// Always apply prefix — never return unprefixed base client
	appID := ""
	if a := auth.Get(ctx); a != nil {
		appID = a.AppID
	}
	return client.ForApp(appID, m.config.ID), releaseClient, nil
}

// resolveCache returns the underlying cache client and a release closure.
// Production uses ClientCache (refcount-pinned). Dev uses a single shared
// client (no-op release).
func (m *Module) resolveCache(ctx context.Context) (*cache.Client, func(), error) {
	if cred := cache.CredentialFrom(ctx); cred != nil {
		return m.cacheCache.Get(ctx, *cred)
	}
	m.devCacheOnce.Do(func() {
		m.devCache, m.devCacheErr = cache.Open(context.Background())
	})
	if m.devCacheErr != nil {
		return nil, nil, m.devCacheErr
	}
	return m.devCache, func() {}, nil
}

// Storage returns a scoped storage client. Keys are auto-prefixed with the app/module path.
//
//	s, err := mod.Storage(r.Context())
//	if err != nil { ... }
//	url, err := s.PresignPut(ctx, "photo.jpg", 15*time.Minute)
//	cdnURL, err := s.URL("photo.jpg")
//
// Prefix and CDN base come from the per-invocation STS credential in production,
// or env vars in dev mode. resolveStorage handles both paths — NewFromCredential
// already sets the prefix from cred.Prefix, so no separate ForApp scoping is needed.
func (m *Module) Storage(ctx context.Context) (storage.Storer, error) {
	return m.resolveStorage(ctx)
}

func (m *Module) resolveStorage(ctx context.Context) (*storage.Client, error) {
	// Production: STS credentials from Lambda payload.
	// No caching — S3 client creation is cheap (no I/O), and STS tokens rotate
	// frequently. Caching by AccessKeyID risks using stale credentials.
	if cred := storage.CredentialFrom(ctx); cred != nil {
		return storage.NewFromCredential(*cred)
	}
	// Dev: env vars
	m.devStorageOnce.Do(func() {
		m.devStorage, m.devStorageErr = storage.Open(context.Background())
	})
	return m.devStorage, m.devStorageErr
}

// Meter DECLARES a usage metric. Call it once, up front (startup code), per
// metric — exactly like Emits / RegisterPermission: it registers the metric as
// a SIDE EFFECT and returns NOTHING. The declaration (name + kind + unit +
// price) is recorded in the manifest so the platform's metric catalog is
// authoritative before any event arrives, AND in the meter client's by-name
// registry so Record can resolve the metric.
//
// kind is meter.Counter (additive; the platform SUMs) or meter.Gauge (absolute
// level; the platform takes MAX or a time-weighted integral, never a SUM).
// Options set the unit and the per-unit customer price (meter.Unit/meter.Price,
// both optional).
//
// Emit at runtime with the package-level Record(ctx, name, value) — BY NAME,
// mirroring Emits/Emit. Panics on a duplicate metric name (a second
// declaration would silently disagree on kind/price), an invalid name, an
// unknown kind, or a reserved infra.*/platform.* prefix. Like the other Module
// resource methods, it requires New() to have returned successfully — calling
// it on a zero Module panics on the nil meterClient (the package-level
// wrapper's mustDefault guards the before-Init case).
//
//	ms.Meter("orders.placed", ms.Counter, ms.Unit("order"), ms.Price(50_000))
func (m *Module) Meter(name string, kind meter.Kind, opts ...meter.MetricOption) {
	d := meter.DeclFromOptions(name, kind, opts...)
	// Declare validates name/kind/reserved-prefix and registers into the meter
	// client's by-name registry (panics on a duplicate name there).
	m.meterClient.Declare(m.config.ID, d)
	decl := registry.MetricDecl{Name: d.Name, Kind: string(d.Kind), Unit: d.Unit}
	if d.PriceSet {
		p := d.Price
		decl.Price = &p
	}
	m.registry.AddMetric(decl)
}

// Record emits a usage event for the metric declared (via Meter) under name —
// BY NAME, mirroring Emit. Resolves the declared metric from the meter client's
// registry; returns an error (never panics) if name was never declared, or if
// value is negative/non-finite. A billing failure must never fail the handler,
// so the error should be logged, not propagated.
func (m *Module) Record(ctx context.Context, name string, value float64) error {
	return m.meterClient.Record(ctx, name, value)
}

// Package-level convenience wrappers — dispatch to defaultModule.

// Cache returns a scoped cache client on the default module.
func Cache(ctx context.Context) (cache.Cacher, func(), error) {
	return mustDefault("Cache").Cache(ctx)
}

// Storage returns a scoped storage client on the default module.
func Storage(ctx context.Context) (storage.Storer, error) {
	return mustDefault("Storage").Storage(ctx)
}

// Meter declares a usage metric on the default module (side effect, no return).
// Panics before Init.
func Meter(name string, kind meter.Kind, opts ...meter.MetricOption) {
	mustDefault("Meter").Meter(name, kind, opts...)
}

// Record emits a usage event by name on the default module. Panics before Init.
func Record(ctx context.Context, name string, value float64) error {
	return mustDefault("Record").Record(ctx, name, value)
}
