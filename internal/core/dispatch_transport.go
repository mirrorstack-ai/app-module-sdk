package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/internal/runtime"
)

// This file is the ONE module->dispatch transport every outbound surface
// (Call, Emit, Notify) builds on: base resolution, the app-scope guard, and
// the POST-with-identity-headers/error contract. Each surface keeps only its
// envelope construction and its resolve*URL path building, so the #146 prod
// transport lands here (and in the per-surface resolvers' path logic) instead
// of being swapped in N copies.
//
// dispatchTTLSeconds is here for the same reason: "ttl is a PROPOSAL, sent as
// whole seconds, absent when unset" is one wire convention shared by every mint
// surface (Delivery, MintMemberAssertion), not a fact about any one of them.

// dispatchFallbackWarn keeps the unset-MS_DISPATCH_URL warning to one line per
// process, so a module that makes many calls does not spam its log.
var dispatchFallbackWarn sync.Once

// dispatchBase resolves the platform-dispatch base URL: MS_DISPATCH_URL (the
// container->dispatch base) with the host.docker.internal dev fallback when
// unset.
func dispatchBase() string {
	base := os.Getenv("MS_DISPATCH_URL")
	if base == "" {
		// The fallback is only correct when a FULL local platform stack is
		// running (its dispatch listens on :8083). A dev-tunnel session runs no
		// local platform, so taking this branch means every ms.Call/Emit/Notify
		// will fail with a bare connection error the caller can only report as a
		// 502. Say so once, loudly, instead of failing mute.
		dispatchFallbackWarn.Do(func() {
			slog.Warn("MS_DISPATCH_URL is unset; falling back to the local dev dispatch. "+
				"Inter-module calls, events and notifications will fail unless a local platform stack is running on this address.",
				"fallback", devDispatchFallback)
		})
		base = devDispatchFallback
	}
	return strings.TrimRight(base, "/")
}

// dispatchBaseFor resolves routing from the broker-attested one-shot context
// before consulting process state. A warm Standard Lambda can execute
// concurrent attempts, so task routing must never be installed through a
// process-global environment mutation.
func dispatchBaseFor(ctx context.Context) string {
	if base := runtime.TaskDispatchURLFrom(ctx); base != "" {
		return strings.TrimRight(base, "/")
	}
	return dispatchBase()
}

// moduleSessionSecret returns the outbound credential that binds this module
// process to an identity dispatch can verify. This is the outbound
// module-identity seam already used by dependency_db.go, meter and the CLI's
// module-log ingest, NOT the inbound MS_PLATFORM_TOKEN hierarchy.
//
// No longer tunnel-only despite the name. MS_INTERNAL_SECRET now carries either
// the per-session secret the CLI mints under `mirrorstack dev --tunnel`, or —
// on a deployed module — the per-module credential the deploy provisioner
// injects, which dispatch recomputes from the platform secret and this module's
// catalog UUID. Both are opaque here and are sent verbatim; dispatch does a
// constant-time compare, so reshaping the value is a silent 403.
//
// The name is kept deliberately: renaming it would churn every call site for no
// behavioral gain. Empty means "no credential" and callers skip the header
// rather than sending it blank.
func moduleSessionSecret() string { return os.Getenv("MS_INTERNAL_SECRET") }

// outboundServiceSecret resolves an attempt-scoped renewable task capability
// before falling back to the process credential used by normal Lambda/dev
// requests. A task provider always wins: refresh denial must fail closed and
// must never fall back to ambient MS_INTERNAL_SECRET.
func outboundServiceSecret(ctx context.Context, operation string) (string, error) {
	if provider := runtime.ModuleCallCapabilityProviderFrom(ctx); provider != nil {
		capability, err := provider.ModuleCallCapability(ctx)
		if err != nil {
			return "", fmt.Errorf("mirrorstack: %s: task module-call capability unavailable: %w", operation, err)
		}
		if capability.Token == "" || (!capability.ExpiresAt.IsZero() && !capability.ExpiresAt.After(time.Now())) {
			return "", fmt.Errorf("mirrorstack: %s: task module-call capability is missing or expired", operation)
		}
		return capability.Token, nil
	}
	return moduleSessionSecret(), nil
}

// dispatchTTLSeconds turns a Duration proposal into the wire field shared by
// every dispatch mint envelope.
//
// nil means "unset", which the platform reads as its default. A negative ttl is
// a caller bug and is rejected rather than quietly defaulted — silently turning
// a wrong lifetime into an hour hides the bug at the one place it is cheap to
// see. A positive sub-second ttl rounds UP to 1s so it stays an explicit
// proposal (the platform then clamps it to its floor) instead of truncating to
// 0 and reading as "unset".
func dispatchTTLSeconds(ttl time.Duration) (*int64, error) {
	switch {
	case ttl < 0:
		return nil, fmt.Errorf("ttl must not be negative (got %s)", ttl)
	case ttl == 0:
		return nil, nil
	}
	seconds := int64(ttl / time.Second)
	if ttl%time.Second != 0 {
		seconds++
	}
	return &seconds, nil
}

// appIDFromContext reads the current app id from the request context — the
// same identity the SDK injects for handlers. An empty app id is an error
// (no panic): every dispatch surface needs an app scope. op names the caller
// ("Emit", "Notify") in the error.
func appIDFromContext(ctx context.Context, op string) (string, error) {
	if a := auth.Get(ctx); a != nil && a.AppID != "" {
		return a.AppID, nil
	}
	return "", fmt.Errorf("mirrorstack: %s requires an app-scoped context (no AppID in auth identity)", op)
}

// postDispatchJSON marshals payload and POSTs it to url with the base module->
// dispatch headers (Content-Type + X-MS-App-ID) plus whatever extraHeaders the
// CALLER opts into. Credentials are per-surface, not blanket: only the routes
// dispatch actually gates on a secret pass one, so a surface can never acquire
// the session credential by accident. An empty value is skipped rather than
// written as a blank header — dispatch reads a blank as "no credential", and a
// blank header is indistinguishable on the wire from an authentication bug.
// Whatever is sent, dispatch still re-derives sender identity itself and never
// trusts an envelope assertion. A non-2xx response is returned as an error
// prefixed with op ("ms.Emit", "ms.Notify"), body truncated to ~2 KB.
func postDispatchJSON(ctx context.Context, op, url, appID string, payload any, extraHeaders map[string]string) error {
	return postDispatchJSONInto(ctx, op, url, appID, payload, nil, extraHeaders)
}

// dispatchResponseError preserves the status class for durable transports
// without changing the long-standing human-readable error returned by Emit,
// Notify, Delivery, and member assertions. Its body is bounded at the read
// site and stays private to this package.
type dispatchResponseError struct {
	op         string
	path       string
	statusCode int
	body       string
}

func (e *dispatchResponseError) Error() string {
	return fmt.Sprintf("%s %s -> %d: %s", e.op, e.path, e.statusCode, e.body)
}

// postDispatchJSONInto is postDispatchJSON for a surface that needs the
// RESPONSE, decoding a 2xx body into out (pass nil to discard it, which is what
// postDispatchJSON does). Everything else — base headers, the opt-in credential
// rule, the non-2xx error contract — is shared, so a response-reading surface
// cannot drift from a fire-and-forget one.
//
// A decode failure is an error, never a partially-populated out: a surface that
// returns a CREDENTIAL must not proceed on half a response.
func postDispatchJSONInto(ctx context.Context, op, url, appID string, payload, out any, extraHeaders map[string]string) error {
	buf, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-MS-App-ID", appID)
	for name, value := range extraHeaders {
		if value != "" {
			req.Header.Set(name, value)
		}
	}

	resp, err := callHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return &dispatchResponseError{
			op: op, path: req.URL.Path, statusCode: resp.StatusCode,
			body: strings.TrimSpace(string(b)),
		}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s %s: decode response: %w", op, req.URL.Path, err)
	}
	return nil
}
