package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
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
// this resolver is the seam where that plugs in, mirroring resolveCallURL. In
// prod the event bus is not a single dev dispatch base; when #146 lands, swap
// the body of this function (and only this function) to consult the prod
// transport. Emit's marshal/auth/error contract stays put.
func resolveEventBusURL(appID, name string) string {
	base := os.Getenv("MS_DISPATCH_URL")
	if base == "" {
		base = devDispatchFallback
	}
	base = strings.TrimRight(base, "/")
	return fmt.Sprintf("%s/apps/%s/events/%s", base, appID, name)
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
	appID := ""
	if a := auth.Get(ctx); a != nil {
		appID = a.AppID
	}
	if appID == "" {
		return errors.New("mirrorstack: Emit requires an app-scoped context (no AppID in auth identity)")
	}

	env := eventEnvelope{
		ID:             ids.NewUUID(),
		Name:           name,
		SourceModuleID: m.config.ID,
		SentAt:         time.Now().UTC().Format(time.RFC3339Nano),
		Payload:        payload,
	}
	buf, err := json.Marshal(env)
	if err != nil {
		return err
	}

	u := resolveEventBusURL(appID, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-MS-App-ID", appID)

	resp, err := callHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("ms.Emit %s -> %d: %s", req.URL.Path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// Emit publishes an event on the default module created by Init(). Panics
// before Init — matches Call/CallGet/CallPost.
func Emit(ctx context.Context, name string, payload any) error {
	return mustDefault("Emit").Emit(ctx, name, payload)
}
