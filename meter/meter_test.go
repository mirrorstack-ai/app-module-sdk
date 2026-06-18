package meter

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
)

// capture records what the dispatch usage ingress received for a Record POST.
type capture struct {
	mu     sync.Mutex
	hits   int
	method string
	path   string
	appID  string
	ct     string
	body   []byte
}

func (c *capture) get() capture {
	c.mu.Lock()
	defer c.mu.Unlock()
	return capture{hits: c.hits, method: c.method, path: c.path, appID: c.appID, ct: c.ct, body: c.body}
}

// newDispatchStub starts an httptest server standing in for the dispatch usage
// ingress, points MS_DISPATCH_URL at it, and returns a client + the capture. The
// status code the stub returns is configurable for the non-2xx test.
func newDispatchStub(t *testing.T, status int) (*Client, *capture) {
	t.Helper()
	cap := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.mu.Lock()
		cap.hits++
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.appID = r.Header.Get("X-MS-App-ID")
		cap.ct = r.Header.Get("Content-Type")
		cap.body, _ = io.ReadAll(r.Body)
		cap.mu.Unlock()
		if status == 0 {
			status = http.StatusAccepted
		}
		w.WriteHeader(status)
		if status >= 300 {
			_, _ = w.Write([]byte("usage ingress unavailable"))
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MS_DISPATCH_URL", srv.URL)
	c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, cap
}

// newTestClient returns a meter client with a real HTTP transport but no
// configured dispatch (it targets whatever MS_DISPATCH_URL / dev fallback
// resolves to). Used by tests that assert on the pre-transport path (validation,
// declaration) and must NOT reach a server — those tests use an undeclared /
// reserved / invalid input so dispatch is never called.
func newTestClient(t *testing.T) *Client {
	t.Helper()
	return &Client{httpClient: &http.Client{}}
}

// declareCounter is a test helper: declares a counter metric on c bound to
// module "media", so Record(ctx, name, ...) resolves it by name.
func declareCounter(t *testing.T, c *Client, name string) {
	t.Helper()
	c.Declare("media", DeclFromOptions(name, Counter))
}

// appCtx is a context carrying an auth identity with the given app id, the
// scope Record needs to attribute usage.
func appCtx(appID string) context.Context {
	return auth.Set(context.Background(), auth.Identity{AppID: appID})
}

func TestNew_RejectsMalformedDispatchURL(t *testing.T) {
	for _, bad := range []string{"://nope", "not a url", "ftp://host", "http://"} {
		t.Run(bad, func(t *testing.T) {
			t.Setenv("MS_DISPATCH_URL", bad)
			if _, err := New(); err == nil {
				t.Errorf("New() should reject MS_DISPATCH_URL=%q", bad)
			}
		})
	}
}

func TestNew_AllowsUnsetDispatchURL(t *testing.T) {
	t.Setenv("MS_DISPATCH_URL", "")
	c, err := New()
	if err != nil {
		t.Fatalf("New() with unset MS_DISPATCH_URL should succeed (dev fallback): %v", err)
	}
	if c.httpClient == nil {
		t.Error("New() must always build the HTTP client (never nil)")
	}
}

func TestResolveUsageURL_Building(t *testing.T) {
	cases := []struct {
		name     string
		dispatch string // "" = unset -> dev fallback
		appID    string
		want     string
	}{
		{"dev fallback when unset", "", "a-456", devDispatchFallback + "/apps/a-456/usage"},
		{"explicit base", "http://dispatch:8083", "a-456", "http://dispatch:8083/apps/a-456/usage"},
		{"trailing slash trimmed", "http://dispatch:8083/", "a-456", "http://dispatch:8083/apps/a-456/usage"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MS_DISPATCH_URL", tc.dispatch)
			if got := resolveUsageURL(tc.appID); got != tc.want {
				t.Errorf("resolveUsageURL(%q) = %q, want %q", tc.appID, got, tc.want)
			}
		})
	}
}

func TestRecord_PostsEventToUsageIngress(t *testing.T) {
	c, cap := newDispatchStub(t, http.StatusAccepted)
	declareCounter(t, c, "transcode.minutes")

	ctx := auth.Set(context.Background(), auth.Identity{AppID: "app_abc", AppRole: "admin"})
	if err := c.Record(ctx, "transcode.minutes", 12); err != nil {
		t.Fatalf("Record: %v", err)
	}

	g := cap.get()
	if g.hits != 1 {
		t.Errorf("usage ingress hit %d times, want 1", g.hits)
	}
	if g.method != http.MethodPost {
		t.Errorf("method = %q, want POST", g.method)
	}
	if g.path != "/apps/app_abc/usage" {
		t.Errorf("path = %q, want /apps/app_abc/usage", g.path)
	}
	if g.appID != "app_abc" {
		t.Errorf("X-MS-App-ID = %q, want app_abc", g.appID)
	}
	if g.ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", g.ct)
	}

	var got Event
	if err := json.Unmarshal(g.body, &got); err != nil {
		t.Fatalf("decode payload: %v (body=%s)", err, g.body)
	}
	if got.V != 1 {
		t.Errorf("envelope version = %d, want 1", got.V)
	}
	if got.EventID == "" {
		t.Error("EventID should be set")
	}
	if got.Metric != "transcode.minutes" {
		t.Errorf("metric = %q, want transcode.minutes", got.Metric)
	}
	if got.Value != 12 {
		t.Errorf("value = %g, want 12", got.Value)
	}
	if got.AppIDHint != "app_abc" {
		t.Errorf("appIdHint = %q, want app_abc", got.AppIDHint)
	}
	if got.ModuleIDHint != "media" {
		t.Errorf("moduleIdHint = %q, want media", got.ModuleIDHint)
	}
	if got.RecordedAtHint.IsZero() {
		t.Error("recordedAtHint should be set")
	}
}

func TestRecord_EmptyAppContextErrorsWithoutHTTP(t *testing.T) {
	c, cap := newDispatchStub(t, http.StatusAccepted)
	declareCounter(t, c, "transcode.minutes")

	// No auth identity on the context -> no AppID -> error, no HTTP call.
	if err := c.Record(context.Background(), "transcode.minutes", 1); err == nil {
		t.Fatal("expected error on empty-app context, got nil")
	}
	if cap.get().hits != 0 {
		t.Error("Record made an HTTP call despite empty app context")
	}
}

func TestRecord_Non2xxReturnsErrorWithTruncatedBody(t *testing.T) {
	c, _ := newDispatchStub(t, http.StatusBadGateway)
	declareCounter(t, c, "transcode.minutes")

	err := c.Record(appCtx("a-456"), "transcode.minutes", 1)
	if err == nil {
		t.Fatal("expected error on non-2xx, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "502") {
		t.Errorf("error %q missing status 502", msg)
	}
	if !strings.Contains(msg, "usage ingress unavailable") {
		t.Errorf("error %q missing upstream body", msg)
	}
	if !strings.Contains(msg, "/apps/a-456/usage") {
		t.Errorf("error %q missing request path", msg)
	}
}

// TestMeter_DeclaresKindAndPriceIntoManifest asserts the declaration carries the
// kind / unit / price the platform populates metric_definitions from (the core
// ms.Meter facade hands this Decl to both registry.AddMetric and the meter
// registry). Declaration must not drop any of kind/unit/price.
func TestMeter_DeclaresKindAndPriceIntoManifest(t *testing.T) {
	t.Parallel()
	d := DeclFromOptions("orders.placed", Counter, Unit("order"), Price(50_000))
	if d.Name != "orders.placed" {
		t.Errorf("name = %q, want orders.placed", d.Name)
	}
	if !d.KindSet || d.Kind != counterKind {
		t.Errorf("kind = %q (set=%v), want counter (set=true)", d.Kind, d.KindSet)
	}
	if d.Unit != "order" {
		t.Errorf("unit = %q, want order", d.Unit)
	}
	if !d.PriceSet || d.Price != 50_000 {
		t.Errorf("price = %d (set=%v), want 50000 (set=true)", d.Price, d.PriceSet)
	}

	// A gauge declared with no price: PriceSet must stay false so the manifest
	// distinguishes a declared 0 from "no price".
	g := DeclFromOptions("myapp.objects.bytes", Gauge, Unit("byte"))
	if !g.KindSet || g.Kind != gaugeKind {
		t.Errorf("kind = %q (set=%v), want gauge (set=true)", g.Kind, g.KindSet)
	}
	if g.PriceSet {
		t.Errorf("PriceSet = true for an undeclared price, want false")
	}
}

// TestMeter_KindIsAnOption asserts the kind is passed as an OPTION (Counter /
// Gauge) and lands on the Decl + the manifest registration. This is the core
// of PR #1b: kind moved from a positional argument to a functional option.
func TestMeter_KindIsAnOption(t *testing.T) {
	t.Parallel()
	c := newTestClient(t)
	// Order-independent: kind option can sit anywhere in the variadic list.
	c.Declare("media", DeclFromOptions("orders.placed", Unit("order"), Counter, Price(50_000)))
	c.mu.RLock()
	got := c.metrics["orders.placed"]
	c.mu.RUnlock()
	if !got.KindSet || got.Kind != counterKind {
		t.Errorf("kind = %q (set=%v), want counter (set=true)", got.Kind, got.KindSet)
	}
}

// TestMeter_RejectsMissingKind asserts a CUSTOM metric declared without a kind
// option panics — the platform must know SUM vs MAX/integral up front.
func TestMeter_RejectsMissingKind(t *testing.T) {
	t.Parallel()
	c := newTestClient(t)
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on a custom metric declared without a kind")
		}
	}()
	c.Declare("media", DeclFromOptions("orders.placed", Unit("order"), Price(50_000)))
}

// TestMeter_RejectsConflictingKinds asserts passing both Counter and Gauge
// panics — a metric has exactly one kind.
func TestMeter_RejectsConflictingKinds(t *testing.T) {
	t.Parallel()
	c := newTestClient(t)
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when both Counter and Gauge are passed")
		}
	}()
	c.Declare("media", DeclFromOptions("orders.placed", Counter, Gauge))
}

// TestMeter_ReservedPriceOnlyAccepted asserts a reserved infra.*/platform.*
// metric declared with PRICE ONLY is accepted (a customer-passthrough override)
// and lands in the registry with NO kind/unit (platform-owned), price set.
func TestMeter_ReservedPriceOnlyAccepted(t *testing.T) {
	t.Parallel()
	c := newTestClient(t)
	c.Declare("media", DeclFromOptions("infra.compute.ms", Price(0)))
	c.mu.RLock()
	got, ok := c.metrics["infra.compute.ms"]
	c.mu.RUnlock()
	if !ok {
		t.Fatal("reserved price-override metric should be declared")
	}
	if got.KindSet || got.Kind != "" {
		t.Errorf("reserved override should carry no kind, got %q (set=%v)", got.Kind, got.KindSet)
	}
	if got.UnitSet {
		t.Error("reserved override should carry no unit")
	}
	if !got.PriceSet || got.Price != 0 {
		t.Errorf("price = %d (set=%v), want 0 (set=true)", got.Price, got.PriceSet)
	}
}

// TestMeter_ReservedWithKindOrUnitPanics asserts a reserved metric carrying a
// kind or a unit option panics — those are platform-owned; a reserved name may
// carry Price only.
func TestMeter_ReservedWithKindOrUnitPanics(t *testing.T) {
	t.Parallel()
	cases := map[string][]MetricOption{
		"kind": {Counter, Price(0)},
		"unit": {Unit("ms"), Price(0)},
	}
	for label, opts := range cases {
		t.Run(label, func(t *testing.T) {
			c := newTestClient(t)
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("expected panic on reserved metric with a %s option", label)
				}
			}()
			c.Declare("media", DeclFromOptions("infra.compute.ms", opts...))
		})
	}
}

// TestMeter_ReservedWithoutPricePanics asserts a reserved metric declared with
// NO options at all is rejected: its only legitimate purpose is to override the
// customer passthrough via ms.Price, so a price-less reserved declaration is a
// meaningless no-op that would otherwise pollute the manifest.
func TestMeter_ReservedWithoutPricePanics(t *testing.T) {
	t.Parallel()
	c := newTestClient(t)
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on a reserved metric declared with no ms.Price")
		}
	}()
	c.Declare("media", DeclFromOptions("infra.compute.ms"))
}

// TestRecord_RejectsReservedName asserts a module can DECLARE a reserved
// price-override but can never self-report its value — ms.Record returns an
// error and never reaches the transport (the platform meters infra itself).
func TestRecord_RejectsReservedName(t *testing.T) {
	c, cap := newDispatchStub(t, http.StatusAccepted)
	c.Declare("media", DeclFromOptions("infra.compute.ms", Price(0)))

	err := c.Record(appCtx("a-456"), "infra.compute.ms", 1)
	if err == nil || !strings.Contains(err.Error(), "platform-measured") {
		t.Errorf("expected a platform-measured rejection, got %v", err)
	}
	if cap.get().hits != 0 {
		t.Errorf("reserved metric must not reach the transport; hits=%d", cap.get().hits)
	}
}

// TestRecord_NoKindOnWire asserts the §4 invariant: kind lives in the manifest,
// NOT on the wire. The serialized Event POSTed to the usage ingress must carry
// no "kind" key (for either a counter or a gauge), the envelope version must
// stay 1, and the value is carried verbatim.
func TestRecord_NoKindOnWire(t *testing.T) {
	for _, kindOpt := range []MetricOption{Counter, Gauge} {
		c, cap := newDispatchStub(t, http.StatusAccepted)
		c.Declare("store", DeclFromOptions("myapp.items", kindOpt))
		if err := c.Record(appCtx("a-456"), "myapp.items", 1); err != nil {
			t.Fatalf("Record: %v", err)
		}
		body := cap.get().body
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(body, &raw); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if _, ok := raw["kind"]; ok {
			t.Errorf("wire envelope must not carry a kind field, got keys %v", keys(raw))
		}
		var got Event
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		if got.V != 1 {
			t.Errorf("envelope version = %d, want 1", got.V)
		}
		if got.Value != 1 {
			t.Errorf("value = %g, want 1", got.Value)
		}
	}
}

func keys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestRecord_RejectsUndeclaredName asserts the declaration-first contract: a
// Record for a name never declared via ms.Meter returns an error and never
// reaches the transport.
func TestRecord_RejectsUndeclaredName(t *testing.T) {
	c, cap := newDispatchStub(t, http.StatusAccepted)
	declareCounter(t, c, "transcode.minutes")

	err := c.Record(appCtx("a-456"), "never.declared", 1)
	if err == nil || !strings.Contains(err.Error(), "never declared") {
		t.Errorf("expected an undeclared-name error, got %v", err)
	}
	if cap.get().hits != 0 {
		t.Errorf("undeclared metric must not reach the transport; hits=%d", cap.get().hits)
	}
}

func TestRecord_RejectsNegativeAndNonFinite(t *testing.T) {
	c, cap := newDispatchStub(t, http.StatusAccepted)
	declareCounter(t, c, "transcode.minutes")

	bad := []float64{-1, math.NaN(), math.Inf(1), math.Inf(-1)}
	for _, v := range bad {
		if err := c.Record(appCtx("a-456"), "transcode.minutes", v); err == nil {
			t.Errorf("Record(%g) should return an error (finite, non-negative)", v)
		}
	}
	if cap.get().hits != 0 {
		t.Errorf("invalid values must not reach the transport; hits=%d", cap.get().hits)
	}
}

// TestMeter_ReservedKindOnAnyPrefixPanics asserts the platform-owned
// infra.*/platform.* namespaces (§3a build rule 3) reject a KIND option across
// every reserved prefix — a module may price-override a reserved metric but can
// never declare its kind (that is platform-owned).
func TestMeter_ReservedKindOnAnyPrefixPanics(t *testing.T) {
	t.Parallel()
	bad := []string{"infra.compute.ms", "infra.egress.bytes", "platform.storage.bytes", "platform.tokens"}
	for _, name := range bad {
		t.Run(name, func(t *testing.T) {
			c := newTestClient(t)
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("expected panic on a kind option for reserved metric %q", name)
				}
			}()
			c.Declare("media", DeclFromOptions(name, Counter))
		})
	}
}

func TestMeter_RejectsInvalidName(t *testing.T) {
	t.Parallel()
	bad := []string{"", "has/slash", "has space", "has..dots", "null\x00byte"}
	for _, name := range bad {
		t.Run(name, func(t *testing.T) {
			c := newTestClient(t)
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("expected panic on invalid metric name %q", name)
				}
			}()
			c.Declare("media", DeclFromOptions(name, Counter))
		})
	}
}

func TestMeter_RejectsInvalidKind(t *testing.T) {
	t.Parallel()
	c := newTestClient(t)
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on invalid kind")
		}
	}()
	// A Decl carrying an out-of-range kind (reachable only by constructing the
	// Decl directly — the public Counter/Gauge options can't produce it) must
	// still be rejected by Declare's IsValid guard.
	c.Declare("media", Decl{Name: "x.y", Kind: Kind("histogram"), KindSet: true})
}

// TestMeter_RejectsDuplicateName asserts a metric declared twice panics — a
// second declaration would silently disagree on kind/price.
func TestMeter_RejectsDuplicateName(t *testing.T) {
	t.Parallel()
	c := newTestClient(t)
	c.Declare("media", DeclFromOptions("orders.placed", Counter))
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on a duplicate metric declaration")
		}
	}()
	c.Declare("media", DeclFromOptions("orders.placed", Gauge))
}

// TestEventID_StableAcrossRetry asserts the §5 invariant: the EventID is minted
// ONCE per Record call and reused across any transport retry, so the platform's
// ON CONFLICT(event_id) dedupe holds and a retried delivery is not
// double-counted. We Record once (mints the EventID), then re-dispatch the same
// built event (simulating a transport retry) and assert the EventID is stable.
func TestEventID_StableAcrossRetry(t *testing.T) {
	c, cap := newDispatchStub(t, http.StatusAccepted)
	declareCounter(t, c, "transcode.minutes")

	// Record once: mints the EventID and POSTs the first attempt.
	if err := c.Record(appCtx("a-456"), "transcode.minutes", 1); err != nil {
		t.Fatalf("Record: %v", err)
	}
	var first Event
	if err := json.Unmarshal(cap.get().body, &first); err != nil {
		t.Fatalf("decode first payload: %v", err)
	}
	if first.EventID == "" {
		t.Fatal("EventID should be set")
	}

	// Retry of the SAME logical call must reuse the SAME EventID and app id.
	// dispatch is the per-attempt transport leg; Record owns the minted event.
	if err := c.dispatch(appCtx("a-456"), "a-456", first); err != nil {
		t.Fatalf("dispatch retry: %v", err)
	}
	var retried Event
	if err := json.Unmarshal(cap.get().body, &retried); err != nil {
		t.Fatalf("decode retry payload: %v", err)
	}
	if retried.EventID != first.EventID {
		t.Errorf("EventID changed across retry: first=%q retry=%q (ON CONFLICT cannot dedupe)", first.EventID, retried.EventID)
	}
}
