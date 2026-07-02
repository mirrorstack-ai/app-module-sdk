package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
)

// This file is the ONE module->dispatch transport every outbound surface
// (Call, Emit, Notify) builds on: base resolution, the app-scope guard, and
// the POST-with-identity-headers/error contract. Each surface keeps only its
// envelope construction and its resolve*URL path building, so the #146 prod
// transport lands here (and in the per-surface resolvers' path logic) instead
// of being swapped in N copies.

// dispatchBase resolves the platform-dispatch base URL: MS_DISPATCH_URL (the
// container->dispatch base) with the host.docker.internal dev fallback when
// unset.
func dispatchBase() string {
	base := os.Getenv("MS_DISPATCH_URL")
	if base == "" {
		base = devDispatchFallback
	}
	return strings.TrimRight(base, "/")
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

// postDispatchJSON marshals payload and POSTs it to url with the module->
// dispatch header set (Content-Type + X-MS-App-ID — no token; the dispatch
// authenticates the sender by transport, never by envelope assertion). A
// non-2xx response is returned as an error prefixed with op ("ms.Emit",
// "ms.Notify"), body truncated to ~2 KB.
func postDispatchJSON(ctx context.Context, op, url, appID string, payload any) error {
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
