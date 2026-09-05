package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	pathpkg "path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/httpx"
	"github.com/mirrorstack-ai/app-module-sdk/ids"
	"github.com/mirrorstack-ai/app-module-sdk/internal/actor"
)

// devDispatchFallback is used when MS_DISPATCH_URL is unset. Modules run inside
// docker; the platform dispatch listens on the host at :8083, reachable via
// host.docker.internal (the same convention docker-compose uses for
// AWS_ENDPOINT_URL). The CLI sets MS_DISPATCH_URL explicitly in dev, and prod
// points it at the real dispatch — this default only keeps local runs working.
const devDispatchFallback = "http://host.docker.internal:8083"

// devModuleProxyFallback is the in-session module multiplexer started by
// `mirrorstack dev --all`. A tunnel deliberately keeps MS_DISPATCH_URL pointed
// at the real platform, so a co-located dependency fallback must not derive its
// endpoint from that variable: doing so sends a local module ID to production
// dispatch and deterministically returns module_not_found.
const devModuleProxyFallback = "http://127.0.0.1:8080"

// dependencyCallRoutedHeader is set by the platform dependency ingress before
// it performs any auth, catalog, or producer work. Its presence proves that a
// 404 came from the ingress (or the producer it forwarded to), not from an old
// edge whose router does not know the module-calls route yet.
const dependencyCallRoutedHeader = "X-MS-Dependency-Call-Routed"

// These are the exact net/http default-router response properties observed at
// an old dispatch edge that predates the dependency-call ingress. Keeping this
// compatibility signature deliberately narrow matters for mutating calls: an
// arbitrary or producer-owned 404 must never cause the POST to run twice.
const (
	legacyRouteMissContentType = "text/plain; charset=utf-8"
	legacyRouteMissBody        = "404 page not found\n"
)

// callTimeout bounds a single inter-module hop. Matches the value the
// hand-rolled oauth-core dispatchclient used.
const callTimeout = 15 * time.Second

// maxDependencyCallBody mirrors the authenticated dependency ingress ceiling.
// Rejecting before transport avoids spending memory and network bandwidth on a
// request the platform will deterministically reject.
const maxDependencyCallBody = 1 << 20

// CallOptions opts one inter-module response into stricter decoding. The zero
// value preserves Call's existing forward-compatible, unbounded response
// behavior.
type CallOptions struct {
	// MaxResponseBytes bounds a successful response before JSON decoding. Zero
	// keeps the existing unbounded behavior. Negative values are invalid.
	MaxResponseBytes int64
	// StrictJSON rejects response object fields not declared by out and rejects
	// a second/trailing JSON value.
	StrictJSON bool
}

// ErrCallResponseTooLarge is returned when a successful inter-module response
// exceeds CallOptions.MaxResponseBytes. Use errors.Is to match it.
var ErrCallResponseTooLarge = errors.New("ms.Call: response exceeds configured limit")

// callHTTP is the shared client for inter-module calls. A single client with a
// per-request timeout is enough — context cancellation still applies on top.
//
// 🔴 IT MUST NOT USE http.DefaultTransport. DefaultTransport caps
// MaxIdleConnsPerHost at 2, and every inter-module call in a module goes to the
// SAME host (dispatch). While a module's fan-out is serial that is invisible —
// concurrency never exceeds 1. The moment a host module fans out to N
// contributors in parallel, connections 3..N cannot be pooled: each one pays a
// fresh TCP handshake (plus TLS in production) and is then CLOSED rather than
// returned, on every request. That converts most of the intended parallel win
// back into handshake latency, silently, and it looks like "parallelising
// didn't help much" rather than like a misconfigured pool.
//
// Cloned from DefaultTransport so proxy handling, timeouts and HTTP/2 stay
// exactly as they were; only the pool sizes change.
var callHTTP = &http.Client{Timeout: callTimeout, Transport: callTransport()}

// callTransport is DefaultTransport with a pool sized for fan-out. 64 is
// comfortably above any realistic contributor count and costs nothing when
// unused — idle connections are reaped by IdleConnTimeout as before.
func callTransport() *http.Transport {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		// Something replaced DefaultTransport. Leave it alone rather than
		// guessing at a replacement.
		return nil
	}
	t = t.Clone()
	t.MaxIdleConnsPerHost = 64
	t.MaxConnsPerHost = 0 // unlimited in-flight; the pool bound is what mattered
	return t
}

// resolveCallURL builds the platform-dispatch URL for a module->module hop:
//
//	{base}/v1/dispatch/apps/module/{targetModuleID}{path}
//
// base is MS_DISPATCH_URL — the bare API host (https://api.<domain>), on which
// /v1/dispatch is path-routed to the dispatch Lambda — with the
// host.docker.internal:8083 dev fallback when unset. The module-keyed dispatch
// edge resolves the app from X-MS-App-ID (else the app_id query, else a
// single-install auto-resolve) and keeps /internal/* open to module callers.
//
// DEV/DISPATCH TRANSPORT. Prod catalog/Lambda endpoint resolution is task
// #146 — this resolver is the seam where that plugs in. In prod the target
// module is not reachable through a single dev dispatch base; the platform
// catalog resolves the installed module's Lambda endpoint per app. When #146
// lands, swap the body of this function (and only this function) to consult
// the catalog/Lambda resolver; Call's marshal/auth/error contract stays put.
func resolveCallURL(targetModuleID, path string) string {
	return resolveCallURLFor(context.Background(), targetModuleID, path)
}

func resolveCallURLFor(ctx context.Context, targetModuleID, path string) string {
	return fmt.Sprintf("%s/v1/dispatch/apps/module/%s%s", dispatchBaseFor(ctx), targetModuleID, path)
}

func resolveDependencyCallURL(appRef, consumerRef, producerRef, path string) string {
	return resolveDependencyCallURLFor(context.Background(), appRef, consumerRef, producerRef, path)
}

func resolveDependencyCallURLFor(ctx context.Context, appRef, consumerRef, producerRef, path string) string {
	return fmt.Sprintf(
		"%s/internal/apps/%s/module-calls/%s/%s%s",
		dispatchBaseFor(ctx),
		url.PathEscape(appRef),
		url.PathEscape(consumerRef),
		url.PathEscape(producerRef),
		path,
	)
}

func resolveDevModuleURL(slug, path string) string {
	base := strings.TrimSpace(os.Getenv("MS_LOCAL_MODULE_PROXY_URL"))
	if base == "" {
		base = devModuleProxyFallback
	}
	return strings.TrimRight(base, "/") + "/_m/" + url.PathEscape(slug) + path
}

// Call makes one server-mediated module-to-module hop through the platform
// dispatch. It marshals body to JSON (omitted for a nil body), POST/GET/etc to
// the resolved dispatch URL scoped to the current app, and decodes the JSON
// response into out (skipped when out is nil).
//
// The app id is read from the request context (auth.Get) — the same identity
// the SDK injects for handlers — and sent as X-MS-App-ID so the dispatch can
// resolve the install. X-MS-Service-Secret carries this CALLING module's live
// session/deploy credential, allowing dispatch to authenticate which installed
// module initiated the internal hop. The caller never holds the CALLEE's
// credentials: dispatch injects the target module's per-session token and
// identity before forwarding.
//
// path must include its leading slash and any raw query string, e.g.
// "/internal/exchange" or "/platform/users?limit=10". A verified actor
// delegation is forwarded only to a canonical /platform route. Public and
// Internal calls remain actorless service calls even when they originate in a
// Platform handler.
//
// DEV/DISPATCH TRANSPORT — see resolveCallURL for the prod (#146) seam.
func (m *Module) Call(ctx context.Context, targetModuleID, method, path string, body, out any) error {
	return m.CallWithOptions(ctx, targetModuleID, method, path, body, out, CallOptions{})
}

// CallWithOptions is Call with opt-in response bounds and strict JSON
// decoding. A negative MaxResponseBytes is rejected before any network I/O.
// When out is nil, a successful response body is not read or decoded.
func (m *Module) CallWithOptions(ctx context.Context, targetModuleID, method, path string, body, out any, options CallOptions) error {
	if options.MaxResponseBytes < 0 {
		return errors.New("ms.CallWithOptions: MaxResponseBytes must be non-negative")
	}
	appID := AppID(ctx)
	return m.callURLWithOptions(ctx, appID, method, resolveCallURLFor(ctx, targetModuleID, path), path, body, out, 0, options)
}

// CallDependency addresses a declared dependency by the same stable ref used
// in DependsOn. The platform authenticates this module, checks the live
// dependency declaration, resolves the producer within the current app, and
// forwards an actorless request to the producer's Internal surface.
func (m *Module) CallDependency(ctx context.Context, producerRef, method, path string, body, out any) error {
	appID := AppID(ctx)
	if appID == "" {
		return errors.New("ms.CallDependency: request context has no app scope")
	}
	if !isInternalCallPath(path) {
		return errors.New("ms.CallDependency: path must be a canonical /internal route")
	}
	producer, err := parseProducerRef(producerRef)
	if err != nil {
		return fmt.Errorf("ms.CallDependency: %w", err)
	}
	// The local direct-route compatibility path must never turn discovery into
	// authority. Reject undeclared producers before either directory or network
	// I/O, but leave deployed authorization to the authenticated platform.
	if m.devMode && !m.declaresDependencyRef(producer) {
		return fmt.Errorf("ms.CallDependency: producer %q is not declared by this module", producer)
	}
	appRef, err := managedTaskAppRef(ctx)
	if err != nil {
		return errors.New("ms.CallDependency: request context has no app scope")
	}
	endpoint := resolveDependencyCallURLFor(ctx, appRef, m.config.ID, producer, path)
	callErr := m.callURL(ctx, appID, method, endpoint, path, body, out, maxDependencyCallBody)
	if callErr == nil || !m.devMode || !isLegacyDependencyIngressRouteMiss(callErr) {
		return callErr
	}

	// Compatibility for a local CLI whose platform dependency ingress has not
	// landed yet: only the exact unmarked default-router 404 may fall back, and
	// only to a declared producer with a live co-located dev lease. Marked ingress
	// responses include producer responses, so even a producer that returns the
	// same 404 text cannot make a mutating call run twice.
	directEndpoint, directToken, handled, err := m.resolveDevDependencyCall(ctx, producer, path)
	if err != nil {
		return err
	}
	if !handled {
		return callErr
	}
	return m.callURLWithCredential(ctx, appID, method, directEndpoint, path, body, out, maxDependencyCallBody, auth.HeaderPlatformToken, directToken)
}

// resolveDevDependencyCall turns a declared dependency into the direct local
// module-proxy route (/_m/{slug}/internal/...) only while both modules are in
// the same leased mirrorstack-dev session. Discovery is never authority: the
// caller's normalized ref must match an explicit DependsOn declaration before
// the directory (and therefore any I/O) is consulted.
//
// handled=false preserves the authenticated platform dependency ingress's 404
// for a declared producer that is remote, whose lease expired, or whose
// directory lookup is temporarily unavailable. Production never enters this
// function, and callers invoke it only after the secure ingress returned 404.
func (m *Module) resolveDevDependencyCall(ctx context.Context, producer, path string) (endpoint, targetToken string, handled bool, err error) {
	if !m.declaresDependencyRef(producer) {
		return "", "", false, fmt.Errorf("ms.CallDependency: producer %q is not declared by this module", producer)
	}

	entry, ok, lookupErr := m.colocatedProducer(ctx, producer)
	if lookupErr != nil {
		m.logger.Printf("dev: dependency directory lookup for %q failed after module-call ingress returned 404: %v", producer, lookupErr)
		return "", "", false, nil
	}
	if !ok {
		return "", "", false, nil
	}
	if !devEntryMatchesRef(entry, producer) {
		return "", "", false, fmt.Errorf("ms.CallDependency: dependency directory entry for %q does not match the declared producer", producer)
	}
	if !moduleIDPattern.MatchString(entry.ModuleID) {
		return "", "", false, fmt.Errorf("ms.CallDependency: dependency directory entry for %q carries invalid module id %q", producer, entry.ModuleID)
	}
	if !ids.ValidModuleSlug(entry.Slug) {
		return "", "", false, fmt.Errorf("ms.CallDependency: dependency directory entry for %q carries invalid module slug %q", producer, entry.Slug)
	}
	token, err := devTargetPlatformToken(entry.Slug)
	if err != nil {
		return "", "", false, err
	}
	return resolveDevModuleURL(entry.Slug, path), token, true, nil
}

// devTargetPlatformToken reads the co-located producer's rotating inbound
// token from the same CLI-owned directory as this module's token. The local
// proxy is only a byte multiplexer; unlike platform dispatch it cannot swap a
// caller credential for the callee credential. Reading on each call preserves
// tunnel reconnect rotation and keeps this fallback limited to modules whose
// live directory entry already matched an explicit DependsOn declaration.
func devTargetPlatformToken(slug string) (string, error) {
	callerFile := strings.TrimSpace(os.Getenv("MS_PLATFORM_TOKEN_FILE"))
	if callerFile == "" {
		return "", errors.New("ms.CallDependency: local producer token directory is unavailable")
	}
	targetFile := filepath.Join(filepath.Dir(callerFile), "ms-platform-token-"+slug)
	raw, err := os.ReadFile(targetFile)
	if err != nil {
		return "", fmt.Errorf("ms.CallDependency: read local producer token: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", errors.New("ms.CallDependency: local producer token is empty")
	}
	return token, nil
}

// declaresDependencyRef compares the request with the registry declaration,
// not with a discovered directory row. parseProducerRef removes owner/version
// decoration and directoryLookupKeys supplies the dashed-UUID <-> m<hex>
// equivalence shared with co-located DependencyDB reads.
func (m *Module) declaresDependencyRef(producer string) bool {
	requested := directoryLookupKeys(producer)
	for _, dependency := range m.registry.Dependencies() {
		ref, err := parseProducerRef(dependency.ID)
		if err != nil {
			continue
		}
		for _, key := range directoryLookupKeys(ref) {
			if slices.Contains(requested, key) {
				return true
			}
		}
	}
	return false
}

func devEntryMatchesRef(entry devModuleEntry, producer string) bool {
	keys := directoryLookupKeys(producer)
	return slices.Contains(keys, entry.Slug) || slices.Contains(keys, entry.ModuleID)
}

func (m *Module) callURL(ctx context.Context, appID, method, endpoint, rawPath string, body, out any, maxBody int) error {
	return m.callURLWithOptions(ctx, appID, method, endpoint, rawPath, body, out, maxBody, CallOptions{})
}

func (m *Module) callURLWithOptions(ctx context.Context, appID, method, endpoint, rawPath string, body, out any, maxBody int, options CallOptions) error {
	secret, err := outboundServiceSecret(ctx, "Call")
	if err != nil {
		return err
	}
	return m.callURLWithCredentialOptions(ctx, appID, method, endpoint, rawPath, body, out, maxBody, "X-MS-Service-Secret", secret, options)
}

func (m *Module) callURLWithCredential(ctx context.Context, appID, method, endpoint, rawPath string, body, out any, maxBody int, credentialHeader, credential string) error {
	return m.callURLWithCredentialOptions(ctx, appID, method, endpoint, rawPath, body, out, maxBody, credentialHeader, credential, CallOptions{})
}

func (m *Module) callURLWithCredentialOptions(ctx context.Context, appID, method, endpoint, rawPath string, body, out any, maxBody int, credentialHeader, credential string, options CallOptions) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		if maxBody > 0 && len(buf) > maxBody {
			return fmt.Errorf("ms.CallDependency: request body size %d exceeds limit of %d bytes", len(buf), maxBody)
		}
		reader = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-MS-App-ID", appID)
	if credential != "" {
		req.Header.Set(credentialHeader, credential)
	}
	if delegation := actor.Delegation(ctx); delegation != "" && isPlatformCallPath(rawPath) {
		req.Header.Set(actor.HeaderDelegation, delegation)
	}

	resp, err := callHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("ms.Call %s %s: %w", method, endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return &callResponseError{
			Method:               method,
			Path:                 req.URL.Path,
			StatusCode:           resp.StatusCode,
			ContentType:          resp.Header.Get("Content-Type"),
			DependencyCallRouted: len(resp.Header.Values(dependencyCallRoutedHeader)) != 0,
			Body:                 string(b),
		}
	}
	if out == nil {
		return nil
	}
	if options.MaxResponseBytes > 0 || options.StrictJSON {
		reader := io.Reader(resp.Body)
		if options.MaxResponseBytes > 0 {
			limit := options.MaxResponseBytes
			if limit < math.MaxInt64 {
				limit++
			}
			reader = io.LimitReader(resp.Body, limit)
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			return fmt.Errorf("ms.Call: read response: %w", err)
		}
		if options.MaxResponseBytes > 0 && int64(len(data)) > options.MaxResponseBytes {
			return fmt.Errorf("%w: limit is %d bytes", ErrCallResponseTooLarge, options.MaxResponseBytes)
		}
		if options.StrictJSON {
			return httpx.UnmarshalStrict(data, out)
		}
		return json.NewDecoder(bytes.NewReader(data)).Decode(out)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// callResponseError retains the HTTP status for narrowly-scoped transport
// decisions without changing the public error string or exposing credentials.
type callResponseError struct {
	Method               string
	Path                 string
	StatusCode           int
	ContentType          string
	DependencyCallRouted bool
	Body                 string
}

func (e *callResponseError) Error() string {
	return fmt.Sprintf("ms.Call %s %s -> %d: %s", e.Method, e.Path, e.StatusCode, strings.TrimSpace(e.Body))
}

func callStatusIs(err error, status int) bool {
	var responseErr *callResponseError
	return errors.As(err, &responseErr) && responseErr.StatusCode == status
}

func isLegacyDependencyIngressRouteMiss(err error) bool {
	var responseErr *callResponseError
	return errors.As(err, &responseErr) &&
		responseErr.StatusCode == http.StatusNotFound &&
		!responseErr.DependencyCallRouted &&
		responseErr.ContentType == legacyRouteMissContentType &&
		responseErr.Body == legacyRouteMissBody
}

// isPlatformCallPath recognizes only canonical module Platform routes. It
// parses the request target before classification so dot-segment or encoded
// traversal cannot make an assertion appear to target /platform while the
// receiving HTTP stack resolves it somewhere else.
func isPlatformCallPath(raw string) bool {
	return isCanonicalCallPath(raw, "/platform")
}

// isInternalCallPath limits declared-dependency calls to service-only module
// routes. Public and Platform routes must never gain actorless service access.
func isInternalCallPath(raw string) bool {
	return isCanonicalCallPath(raw, "/internal")
}

func isCanonicalCallPath(raw, prefix string) bool {
	u, err := url.ParseRequestURI(raw)
	if err != nil || u.IsAbs() || u.Host != "" || u.Fragment != "" {
		return false
	}
	// Classify the PATH only, never the query. A query value legitimately carries
	// %2f/%2e/%5c — an encoded redirect_uri, a base64 blob, a signed token — and
	// scanning the whole target let one of those veto classification: ms.Call
	// then silently dropped the actor delegation header and ms.CallDependency
	// hard-rejected the call. This mirrors the platform ingress, which canonicalizes
	// the path and treats the query as opaque.
	lowerPath := strings.ToLower(u.EscapedPath())
	if strings.Contains(u.Path, "\\") || strings.Contains(lowerPath, "%2f") ||
		strings.Contains(lowerPath, "%2e") || strings.Contains(lowerPath, "%23") ||
		strings.Contains(lowerPath, "%5c") {
		return false
	}
	cleaned := pathpkg.Clean(u.Path)
	if cleaned != u.Path && !(u.Path == prefix+"/" && cleaned == prefix) {
		return false
	}
	return u.Path == prefix || strings.HasPrefix(u.Path, prefix+"/")
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

// CallDependencyPost is CallDependency specialized to POST with a JSON body.
func (m *Module) CallDependencyPost(ctx context.Context, producerRef, path string, body, out any) error {
	return m.CallDependency(ctx, producerRef, http.MethodPost, path, body, out)
}

// Package-level convenience wrappers — dispatch to defaultModule.

// Call makes one inter-module hop through the platform dispatch on the default
// module. Panics before Init.
func Call(ctx context.Context, targetModuleID, method, path string, body, out any) error {
	return mustDefault("Call").Call(ctx, targetModuleID, method, path, body, out)
}

// CallWithOptions makes one inter-module hop with opt-in bounded/strict
// response decoding on the default module. Panics before Init.
func CallWithOptions(ctx context.Context, targetModuleID, method, path string, body, out any, options CallOptions) error {
	return mustDefault("CallWithOptions").CallWithOptions(ctx, targetModuleID, method, path, body, out, options)
}

// CallGet is Call specialized to GET on the default module. Panics before Init.
func CallGet(ctx context.Context, targetModuleID, path string, out any) error {
	return mustDefault("CallGet").CallGet(ctx, targetModuleID, path, out)
}

// CallPost is Call specialized to POST on the default module. Panics before Init.
func CallPost(ctx context.Context, targetModuleID, path string, body, out any) error {
	return mustDefault("CallPost").CallPost(ctx, targetModuleID, path, body, out)
}

// CallDependencyPost addresses a declared dependency by its stable ref.
func CallDependencyPost(ctx context.Context, producerRef, path string, body, out any) error {
	return mustDefault("CallDependencyPost").CallDependencyPost(ctx, producerRef, path, body, out)
}

// identity returns the context's auth identity, or the zero Identity when
// none is set, so field reads on it degrade to "".
func identity(ctx context.Context) auth.Identity {
	if a := auth.Get(ctx); a != nil {
		return *a
	}
	return auth.Identity{}
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
	return identity(ctx).AppID
}

// UserID returns the user id from the request context's auth identity, or ""
// if no identity is set. Same promotion paths and context-not-module shape as
// AppID; "" is legitimate on surfaces with no user (internal/system/cron/task
// invocations, anonymous Public requests).
func UserID(ctx context.Context) string {
	return identity(ctx).UserID
}

// AppRole returns the app role from the request context's auth identity, or
// "" if no identity is set. Same promotion paths and context-not-module shape
// as AppID; "" is legitimate on surfaces with no user (internal/system/cron/
// task invocations, anonymous Public requests).
func AppRole(ctx context.Context) string {
	return identity(ctx).AppRole
}
