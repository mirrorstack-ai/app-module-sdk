package meter

import (
	"context"
	"encoding/json"
	"errors"
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
	secret []string
	body   []byte
}

func (c *capture) get() capture {
	c.mu.Lock()
	defer c.mu.Unlock()
	return capture{hits: c.hits, method: c.method, path: c.path, appID: c.appID, ct: c.ct, secret: c.secret, body: c.body}
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
		cap.secret = r.Header.Values("X-MS-Service-Secret")
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

func TestRecordWithID_ReusesCallerPersistedEventID(t *testing.T) {
	c, capture := newDispatchStub(t, http.StatusAccepted)
	declareCounter(t, c, "transcode.minutes")
	const eventID = "b9c6a0f0-1234-4abc-8def-0123456789ab"

	for attempt := 0; attempt < 2; attempt++ {
		if err := c.RecordWithID(appCtx("a-456"), eventID, "transcode.minutes", 12); err != nil {
			t.Fatalf("RecordWithID attempt %d: %v", attempt+1, err)
		}
		var got Event
		if err := json.Unmarshal(capture.get().body, &got); err != nil {
			t.Fatalf("decode attempt %d: %v", attempt+1, err)
		}
		if got.EventID != eventID {
			t.Errorf("attempt %d EventID=%q, want persisted %q", attempt+1, got.EventID, eventID)
		}
	}
	if got := capture.get().hits; got != 2 {
		t.Fatalf("usage ingress hit %d times, want 2 retries with one dedup key", got)
	}
}

func TestRecordWithID_RejectsNonCanonicalUUIDWithoutHTTP(t *testing.T) {
	c, capture := newDispatchStub(t, http.StatusAccepted)
	declareCounter(t, c, "transcode.minutes")
	for _, eventID := range []string{
		"", "not-a-uuid", "B9C6A0F0-1234-4ABC-8DEF-0123456789AB",
		"00000000-0000-0000-0000-000000000000",
		"b9c6a0f0-1234-0abc-8def-0123456789ab",
		"b9c6a0f0-1234-9abc-8def-0123456789ab",
		"b9c6a0f0-1234-4abc-7def-0123456789ab",
	} {
		if err := c.RecordWithID(appCtx("a-456"), eventID, "transcode.minutes", 1); err == nil {
			t.Errorf("RecordWithID(%q) succeeded, want validation error", eventID)
		}
	}
	if got := capture.get().hits; got != 0 {
		t.Fatalf("invalid IDs caused %d HTTP calls, want 0", got)
	}
}

// The usage ingress is the billable one: dispatch requires this header to match
// the module's live tunnel session, so dropping it makes every ms.Record under
// `mirrorstack dev --tunnel` 403 and silently stop metering.
func TestRecord_SendsModuleSessionSecret(t *testing.T) {
	t.Setenv("MS_INTERNAL_SECRET", "session-secret-1")
	c, cap := newDispatchStub(t, http.StatusAccepted)
	declareCounter(t, c, "transcode.minutes")

	if err := c.Record(appCtx("a-456"), "transcode.minutes", 1); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if got := cap.get().secret; len(got) != 1 || got[0] != "session-secret-1" {
		t.Errorf("X-MS-Service-Secret values = %q, want [session-secret-1]", got)
	}
}

// A deployed module has no tunnel session secret. Metering is non-fatal by
// contract, so an unset secret must still POST (and let dispatch reject) rather
// than error early or put a blank header on the wire.
func TestRecord_WithoutModuleSessionSecretStillPosts(t *testing.T) {
	t.Setenv("MS_INTERNAL_SECRET", "")
	c, cap := newDispatchStub(t, http.StatusAccepted)
	declareCounter(t, c, "transcode.minutes")

	if err := c.Record(appCtx("a-456"), "transcode.minutes", 1); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got := cap.get()
	if got.hits != 1 {
		t.Errorf("usage ingress hit %d times, want 1", got.hits)
	}
	if len(got.secret) != 0 {
		t.Errorf("X-MS-Service-Secret values = %q, want no header", got.secret)
	}
}

type testServiceCredentialProvider struct {
	secret string
	err    error
}

func (p testServiceCredentialProvider) ServiceCredential(context.Context) (string, error) {
	return p.secret, p.err
}

func TestRecord_TaskProviderOverridesAmbientCredentialAndFailsClosed(t *testing.T) {
	t.Run("provider wins", func(t *testing.T) {
		t.Setenv("MS_INTERNAL_SECRET", "ambient-must-not-win")
		c, cap := newDispatchStub(t, http.StatusAccepted)
		declareCounter(t, c, "transcode.minutes")
		ctx := WithServiceCredentialProvider(appCtx("a-456"), testServiceCredentialProvider{secret: "attempt-capability"})

		if err := c.Record(ctx, "transcode.minutes", 1); err != nil {
			t.Fatalf("Record: %v", err)
		}
		if got := cap.get().secret; len(got) != 1 || got[0] != "attempt-capability" {
			t.Fatalf("X-MS-Service-Secret values = %q, want attempt capability", got)
		}
	})

	t.Run("refresh denial never falls back", func(t *testing.T) {
		t.Setenv("MS_INTERNAL_SECRET", "ambient-must-not-win")
		c, cap := newDispatchStub(t, http.StatusAccepted)
		declareCounter(t, c, "transcode.minutes")
		ctx := WithServiceCredentialProvider(appCtx("a-456"), testServiceCredentialProvider{err: errors.New("refresh denied")})

		err := c.Record(ctx, "transcode.minutes", 1)
		if err == nil || !strings.Contains(err.Error(), "task service credential unavailable") {
			t.Fatalf("Record error = %v", err)
		}
		if cap.get().hits != 0 {
			t.Fatal("usage ingress was reached after task credential refresh denial")
		}
		if strings.Contains(err.Error(), "ambient-must-not-win") {
			t.Fatalf("error leaked ambient credential: %v", err)
		}
	})
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

func TestMeter_BySubjectIsGaugeOnly(t *testing.T) {
	t.Parallel()

	valid := DeclFromOptions("users.active", Gauge, BySubject)
	if valid.AggregationKey != aggregationKeySubject {
		t.Fatalf("aggregation key = %q, want %q", valid.AggregationKey, aggregationKeySubject)
	}
	c := newTestClient(t)
	c.Declare("user-core", valid)

	tests := map[string]Decl{
		"counter":  DeclFromOptions("users.counter", Counter, BySubject),
		"reserved": DeclFromOptions("infra.users.active", Price(0), BySubject),
		"no kind":  DeclFromOptions("users.untyped", BySubject),
		"unknown":  {Name: "users.unknown", Kind: gaugeKind, KindSet: true, AggregationKey: "metadata.user"},
	}
	for name, decl := range tests {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("invalid %s aggregation declaration did not panic", name)
				}
			}()
			newTestClient(t).Declare("user-core", decl)
		})
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

// provisionedCredential stands in for what the deploy provisioner injects as
// MS_INTERNAL_SECRET on a DEPLOYED module: the per-module credential dispatch
// recomputes from the platform secret and the module's catalog UUID. Its
// internal structure is deliberately not modelled here — the SDK neither
// derives nor parses it, and duplicating the platform's derivation format in
// this repo would create a second copy to drift.
const provisionedCredential = "3f5a9c1e7b2d48a6f0c3e8b5d1a4720965fe83cb47d20a19e6b8f3c5d7a10e42"

// The fixture itself must stay a realistic opaque credential: 64 lowercase hex
// characters. If someone "tidies" it into something shorter or upper-cased, the
// verbatim-passthrough test below stops proving anything about real values.
func TestProvisionedCredentialFixtureIsOpaqueHex(t *testing.T) {
	if len(provisionedCredential) != 64 {
		t.Fatalf("fixture length = %d, want 64", len(provisionedCredential))
	}
	for _, r := range provisionedCredential {
		if strings.IndexRune("0123456789abcdef", r) < 0 {
			t.Fatalf("fixture contains non-lowercase-hex rune %q", r)
		}
	}
}

// THE DEPLOYED-PLANE WIRE CONTRACT. A deployed module has no tunnel session; it
// authenticates with the credential the provisioner injected. Dispatch does a
// constant-time BYTE compare against a value it recomputes independently, so
// the SDK must forward the env value verbatim — any trimming, case-folding,
// re-encoding or prefixing here is an unattributable 403 in production, and
// metering fails silently by contract.
func TestRecord_SendsProvisionedModuleCredentialVerbatim(t *testing.T) {
	t.Setenv("MS_INTERNAL_SECRET", provisionedCredential)
	c, cap := newDispatchStub(t, http.StatusAccepted)
	declareCounter(t, c, "transcode.minutes")

	if err := c.Record(appCtx("a-456"), "transcode.minutes", 1); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got := cap.get().secret
	// Exactly one value: a duplicated header is ambiguous on the wire and some
	// proxies forward only the first.
	if len(got) != 1 {
		t.Fatalf("X-MS-Service-Secret values = %q, want exactly 1", got)
	}
	if got[0] != provisionedCredential {
		t.Errorf("X-MS-Service-Secret = %q, want the env value forwarded byte-for-byte (%q)", got[0], provisionedCredential)
	}
}

// The credential must never reach a log line or an error string: ms.Record's
// error is what a module logs on the non-fatal metering path, so leaking it
// there publishes the module's callback credential into CloudWatch.
func TestRecord_NeverLeaksCredentialIntoError(t *testing.T) {
	t.Run("non-2xx response", func(t *testing.T) {
		t.Setenv("MS_INTERNAL_SECRET", provisionedCredential)
		c, _ := newDispatchStub(t, http.StatusForbidden)
		declareCounter(t, c, "transcode.minutes")

		err := c.Record(appCtx("a-456"), "transcode.minutes", 1)
		if err == nil {
			t.Fatal("expected an error on a 403 usage ingress")
		}
		if strings.Contains(err.Error(), provisionedCredential) {
			t.Errorf("error leaks the module credential: %v", err)
		}
	})

	t.Run("transport failure", func(t *testing.T) {
		t.Setenv("MS_INTERNAL_SECRET", provisionedCredential)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted)
		}))
		t.Setenv("MS_DISPATCH_URL", srv.URL)
		c, err := New()
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		// Close before recording so the POST fails at the transport layer —
		// the path where net/http builds an error out of the request.
		srv.Close()
		declareCounter(t, c, "transcode.minutes")

		err = c.Record(appCtx("a-456"), "transcode.minutes", 1)
		if err == nil {
			t.Fatal("expected a transport error against a closed server")
		}
		if strings.Contains(err.Error(), provisionedCredential) {
			t.Errorf("transport error leaks the module credential: %v", err)
		}
	})
}
