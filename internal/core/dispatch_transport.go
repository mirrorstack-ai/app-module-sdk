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

	"github.com/mirrorstack-ai/app-module-sdk/auth"
)

// This file is the ONE module->dispatch transport every outbound surface
// (Call, Emit, Notify) builds on: base resolution, the app-scope guard, and
// the POST-with-identity-headers/error contract. Each surface keeps only its
// envelope construction and its resolve*URL path building, so the #146 prod
// transport lands here (and in the per-surface resolvers' path logic) instead
// of being swapped in N copies.

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

// moduleSessionSecret returns the dev-tunnel credential that binds this module
// process to the same live session dispatch registered for it. This is the
// outbound session-identity seam already used by dependency_db.go and the
// CLI's module-log ingest, not the inbound MS_PLATFORM_TOKEN hierarchy.
func moduleSessionSecret() string { return os.Getenv("MS_INTERNAL_SECRET") }

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
		return fmt.Errorf("%s %s -> %d: %s", op, req.URL.Path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}
