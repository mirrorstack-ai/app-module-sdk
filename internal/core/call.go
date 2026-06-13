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
	"time"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
)

// devDispatchFallback is used when MS_DISPATCH_URL is unset. Modules run inside
// docker; the platform dispatch listens on the host at :8083, reachable via
// host.docker.internal (the same convention docker-compose uses for
// AWS_ENDPOINT_URL). The CLI sets MS_DISPATCH_URL explicitly in dev, and prod
// points it at the real dispatch — this default only keeps local runs working.
const devDispatchFallback = "http://host.docker.internal:8083"

// callTimeout bounds a single inter-module hop. Matches the value the
// hand-rolled oauth-core dispatchclient used.
const callTimeout = 15 * time.Second

// callHTTP is the shared client for inter-module calls. A single client with a
// per-request timeout is enough — context cancellation still applies on top.
var callHTTP = &http.Client{Timeout: callTimeout}

// resolveCallURL builds the platform-dispatch URL for a module->module hop:
//
//	{base}/module/{targetModuleID}{path}
//
// base is MS_DISPATCH_URL (the container->dispatch base) with the
// host.docker.internal:8083 dev fallback when unset.
//
// DEV/DISPATCH TRANSPORT. Prod catalog/Lambda endpoint resolution is task
// #146 — this resolver is the seam where that plugs in. In prod the target
// module is not reachable through a single dev dispatch base; the platform
// catalog resolves the installed module's Lambda endpoint per app. When #146
// lands, swap the body of this function (and only this function) to consult
// the catalog/Lambda resolver; Call's marshal/auth/error contract stays put.
func resolveCallURL(targetModuleID, path string) string {
	base := os.Getenv("MS_DISPATCH_URL")
	if base == "" {
		base = devDispatchFallback
	}
	base = strings.TrimRight(base, "/")
	return fmt.Sprintf("%s/module/%s%s", base, targetModuleID, path)
}

// Call makes one server-mediated module-to-module hop through the platform
// dispatch. It marshals body to JSON (omitted for a nil body), POST/GET/etc to
// the resolved dispatch URL scoped to the current app, and decodes the JSON
// response into out (skipped when out is nil).
//
// The app id is read from the request context (auth.Get) — the same identity
// the SDK injects for handlers — and sent as X-MS-App-ID so the dispatch can
// resolve the install. The CALLER never holds the callee's credentials:
// dispatch injects the TARGET module's per-session token + identity before
// forwarding.
//
// path must include its leading slash and any raw query string, e.g.
// "/internal/exchange" or "/internal/users?limit=10".
//
// DEV/DISPATCH TRANSPORT — see resolveCallURL for the prod (#146) seam.
func (m *Module) Call(ctx context.Context, targetModuleID, method, path string, body, out any) error {
	appID := ""
	if a := auth.Get(ctx); a != nil {
		appID = a.AppID
	}

	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(buf)
	}

	u := resolveCallURL(targetModuleID, path)
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-MS-App-ID", appID)

	resp, err := callHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("ms.Call %s %s -> %d: %s", method, req.URL.Path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// CallGet is Call specialized to GET (no request body). path carries any raw
// query string, e.g. "/internal/users?limit=10".
func (m *Module) CallGet(ctx context.Context, targetModuleID, path string, out any) error {
	return m.Call(ctx, targetModuleID, http.MethodGet, path, nil, out)
}

// CallPost is Call specialized to POST with a JSON body.
func (m *Module) CallPost(ctx context.Context, targetModuleID, path string, body, out any) error {
	return m.Call(ctx, targetModuleID, http.MethodPost, path, body, out)
}

// Package-level convenience wrappers — dispatch to defaultModule.

// Call makes one inter-module hop through the platform dispatch on the default
// module. Panics before Init.
func Call(ctx context.Context, targetModuleID, method, path string, body, out any) error {
	return mustDefault("Call").Call(ctx, targetModuleID, method, path, body, out)
}

// CallGet is Call specialized to GET on the default module. Panics before Init.
func CallGet(ctx context.Context, targetModuleID, path string, out any) error {
	return mustDefault("CallGet").CallGet(ctx, targetModuleID, path, out)
}

// CallPost is Call specialized to POST on the default module. Panics before Init.
func CallPost(ctx context.Context, targetModuleID, path string, body, out any) error {
	return mustDefault("CallPost").CallPost(ctx, targetModuleID, path, body, out)
}

// AppID returns the app id from the request context's auth identity, or "" if
// no identity is set. It is the inbound twin of WithAppID and the single
// unspoofable way a handler reads its own app: the SDK promotes the trusted,
// dispatch-injected app id into the identity (Platform via PlatformAuth, Public
// via the proxy guard's success path; Lambda via runtime.InjectResources).
// Reading request data (query/body) for the app id instead is forgeable.
//
// Not module-bound (no *Module receiver) — identity lives on the context, so
// this works before Init and in tests.
func AppID(ctx context.Context) string {
	if a := auth.Get(ctx); a != nil {
		return a.AppID
	}
	return ""
}
