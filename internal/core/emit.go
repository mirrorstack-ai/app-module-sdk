package core

import (
	"context"
	"fmt"
	"time"

	"github.com/mirrorstack-ai/app-module-sdk/internal/ids"
)

// eventEnvelope is the wire shape every emitted event takes. The dispatch
// forwards this struct byte-for-byte to each LIVE subscriber session, so the
// JSON field names here are a SHARED CONTRACT with the dispatch and the
// subscriber-side /__mirrorstack/events/{name} handler — don't rename them
// without changing both sides.
type eventEnvelope struct {
	ID             string `json:"id"`             // per-emit UUID (idempotency key for at-least-once delivery)
	Name           string `json:"name"`           // event name as passed to Emit
	SourceModuleID string `json:"sourceModuleID"` // emitting module's Config.ID
	SentAt         string `json:"sentAt"`         // RFC3339 UTC timestamp of emission
	Payload        any    `json:"payload"`        // caller-supplied payload, marshaled as-is
}

// resolveEventBusURL builds the platform-dispatch URL the envelope is POSTed to:
//
//	{base}/apps/{appID}/events/{name}
//
// base is MS_DISPATCH_URL (the container->dispatch base) with the
// host.docker.internal:8083 dev fallback when unset — the same resolution
// core.Call uses for inter-module hops.
//
// DEV/DISPATCH TRANSPORT. Prod event-bus endpoint resolution is task #146 —
// this resolver (path) and dispatchBase (base) are the seams where that plugs
// in, mirroring resolveCallURL. Emit's marshal/auth/error contract
// (dispatch_transport.go) stays put.
func resolveEventBusURL(appID, name string) string {
	return fmt.Sprintf("%s/apps/%s/events/%s", dispatchBase(), appID, name)
}

// Emit publishes an event to every LIVE module that subscribes to name within
// the current app. It wraps payload in the event envelope (a fresh UUID id, the
// emitting module's Config.ID as sourceModuleID, and an RFC3339 UTC sentAt),
// then POSTs the envelope to the platform dispatch event bus scoped to the
// current app. The dispatch fans the SAME envelope out to each subscriber
// session — the emitter never addresses subscribers directly.
//
// The app id is read from the request context (auth.Get) — the same identity
// the SDK injects for handlers — and is encoded in the dispatch URL. An empty
// app id is an error (no panic): Emit needs an app scope to know which installs
// can receive the event.
//
// Delivery is AT-LEAST-ONCE and best-effort per subscriber (the dispatch logs
// per-subscriber failures and never fails the whole call); subscriber handlers
// must be idempotent. A non-2xx response from the dispatch itself is returned
// as an error with the response body truncated to ~2 KB.
//
// DEV/DISPATCH TRANSPORT — see resolveEventBusURL for the prod (#146) seam.
func (m *Module) Emit(ctx context.Context, name string, payload any) error {
	appID, err := appIDFromContext(ctx, "Emit")
	if err != nil {
		return err
	}

	env := eventEnvelope{
		ID:             ids.NewUUID(),
		Name:           name,
		SourceModuleID: m.config.ID,
		SentAt:         time.Now().UTC().Format(time.RFC3339Nano),
		Payload:        payload,
	}
	return postDispatchJSON(ctx, "ms.Emit", resolveEventBusURL(appID, name), appID, env)
}

// Emit publishes an event on the default module created by Init(). Panics
// before Init — matches Call/CallGet/CallPost.
func Emit(ctx context.Context, name string, payload any) error {
	return mustDefault("Emit").Emit(ctx, name, payload)
}
