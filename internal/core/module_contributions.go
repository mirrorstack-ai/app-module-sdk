// Package mirrorstack is the Go SDK for building modules on MirrorStack.
//
// Use Init() + Start() for the convenience API, or New() for testing and advanced use.
package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/mirrorstack-ai/app-module-sdk/internal/contributions"
)

// This file holds contribution slots: declaring them, and reading what others registered.
//
// Split out of a single 1069-line module.go — the SDK's own version of the
// catch-all this codebase tells module authors not to write.

// built once at the call site and the Module just stores the
// resulting Slot.
func (m *Module) ProvideSlot(slot contributions.Slot) {
	if err := m.contribReg.Define(slot); err != nil {
		panic("mirrorstack: Provide: " + err.Error())
	}
}

// NewContributionSlot builds a Slot whose validator unmarshals into
// T with DisallowUnknownFields so contributors can't sneak in extra
// fields the host doesn't expect (silent jsonb pollution). Schema
// tag is the Go type name, surfaced on the manifest.
func NewContributionSlot[T any](key string) contributions.Slot {
	var zero T
	schemaTag := fmt.Sprintf("%T", zero)
	payloadSchema, err := derivePayloadSchema[T]()
	if err != nil {
		panic("mirrorstack: Provide(" + key + ") payload schema derivation failed: " + err.Error())
	}
	return contributions.NewSlot(key, schemaTag, payloadSchema, func(data json.RawMessage) error {
		var v T
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		return dec.Decode(&v)
	})
}

// Provide is the top-level entry point modules call from
// main.go to declare an extension slot others contribute to:
//
//	ms.Provide[ProviderContribution]("providers")
//
// Builds the type-aware Slot and stores it on the default module.
// Panics on duplicate key or before ms.Init — matches the existing
// RegisterUI / RequirePermission startup-error conventions.
func Provide[T any](key string) {
	mustDefault("Provide").ProvideSlot(NewContributionSlot[T](key))
}

// Contributions returns every contribution slot the default module
// has declared. Surfaced for the manifest builder + tests.
func Contributions() []contributions.SlotInfo {
	if defaultModule == nil {
		return nil
	}
	return defaultModule.contribReg.List()
}

// StoredContributions returns the contributions OTHER modules have registered
// into one of THIS module's slots (slot key as declared via Provide),
// newest first. It reads the SDK-managed <moduleID>_contributions store — the
// canonical registry the platform's install-time auto-register writes to. Each
// entry's ID is the contributing module's id (the registration key), which a
// host can use to address that module. Use this on the HOST side to consume
// contributions (e.g. oauth-core listing its registered auth providers).
//
// A host installed before the SDK provisioned this store on install has no
// table for the app yet; the first read creates it (see Storage.WithTable) and
// returns empty, instead of failing the host with a missing-relation error it
// has no way to act on.
func (m *Module) StoredContributions(ctx context.Context, slot string) ([]contributions.Contribution, error) {
	// m.DB, not the package-level DB: that one resolves through the DEFAULT
	// module, so a non-default *Module would read another module's pool.
	q, release, err := m.DB(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	var out []contributions.Contribution
	if err := m.contribStorage.WithTable(ctx, q, func() error {
		var listErr error
		out, listErr = m.contribStorage.List(ctx, q, slot)
		return listErr
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// StoredContributions reads stored contributions for a slot on the default
// module. Panics if Init has not been called. See Module.StoredContributions.
func StoredContributions(ctx context.Context, slot string) ([]contributions.Contribution, error) {
	return mustDefault("StoredContributions").StoredContributions(ctx, slot)
}

// localDevCORS echoes the request Origin (with credentials) and answers
// OPTIONS preflights. Installed on the module router only when no
// platform-secret source is configured (auth.SecretConfigured() == false) —
// gated parallel to the auth/proxy-guard bypasses so it is never active in
// tunnel/prod, regardless of which secret env var carries the token.
func localDevCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Add("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-MS-Internal-Secret")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
