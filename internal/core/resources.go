package core

import (
	"context"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/cache"
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

// Meter returns a scoped meter for recording usage events (billing metrics).
// Unlike DB/Cache/Storage, Meter returns the interface directly — there is
// no release closure (nothing to release) and no construction error
// (init errors happen eagerly in New()).
//
// In production (MS_METER_LAMBDA_ARN set), Record dispatches to the platform
// meter Lambda via async invoke (~5-15ms per call). In dev mode, Record
// logs to stderr.
//
//	if err := ms.Meter(r.Context()).Record("transcode.minutes", 12); err != nil {
//	    log.Printf("meter: %v", err) // don't fail the handler
//	}
func (m *Module) Meter(ctx context.Context) meter.Meter {
	return m.meterClient.Scope(ctx, m.config.ID)
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

// Meter returns a scoped meter for recording usage events on the default module.
// Panics before Init.
func Meter(ctx context.Context) meter.Meter {
	return mustDefault("Meter").Meter(ctx)
}
