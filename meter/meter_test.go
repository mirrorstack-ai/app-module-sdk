package meter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"math"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
)

type fakeLambda struct {
	invoked int
	lastIn  *lambda.InvokeInput
	err     error
}

func (f *fakeLambda) Invoke(ctx context.Context, in *lambda.InvokeInput, _ ...func(*lambda.Options)) (*lambda.InvokeOutput, error) {
	f.invoked++
	f.lastIn = in
	if f.err != nil {
		return nil, f.err
	}
	return &lambda.InvokeOutput{StatusCode: 202}, nil
}

func newTestClient(t *testing.T, fake *fakeLambda) *Client {
	t.Helper()
	return &Client{
		lambdaClient: fake,
		functionARN:  "arn:aws:lambda:us-east-1:123456789012:function:meter-test",
	}
}

// declareCounter is a test helper: declares a counter metric on c bound to
// module "media", so Record(ctx, name, ...) resolves it by name.
func declareCounter(t *testing.T, c *Client, name string) {
	t.Helper()
	c.Declare("media", DeclFromOptions(name, Counter))
}

func TestNewFromARN_ValidARN(t *testing.T) {
	// config.LoadDefaultConfig probes IMDS for region/credentials; disable it so
	// the test is hermetic and skips the ~100ms IMDS round-trip in CI/sandboxes
	// without AWS credentials. (Not t.Parallel: t.Setenv forbids parallel.)
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	_, err := NewFromARN(context.Background(), "arn:aws:lambda:us-east-1:123456789012:function:meter")
	if err != nil {
		t.Fatalf("NewFromARN with valid ARN: %v", err)
	}
}

func TestNewFromARN_InvalidARN(t *testing.T) {
	t.Parallel()
	cases := []string{
		"",
		"not-an-arn",
		"arn:aws:s3:::bucket/key", // wrong service
		"arn:aws:lambda::123456789012:function:meter",             // missing region
		"arn:aws:lambda:us-east-1::function:meter",                // missing account
		"arn:aws:lambda:us-east-1:123456789012:function:",         // missing name
		"arn:aws:lambda:us-east-1:123456789012:function:bad name", // space in name
	}
	for _, arn := range cases {
		t.Run(arn, func(t *testing.T) {
			_, err := NewFromARN(context.Background(), arn)
			if err == nil {
				t.Errorf("NewFromARN(%q) should reject invalid ARN", arn)
			}
		})
	}
}

func TestRecord_ProdInvokesLambda(t *testing.T) {
	t.Parallel()
	fake := &fakeLambda{}
	c := newTestClient(t, fake)
	declareCounter(t, c, "transcode.minutes")

	ctx := auth.Set(context.Background(), auth.Identity{AppID: "app_abc", AppRole: "admin"})
	if err := c.Record(ctx, "transcode.minutes", 12); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if fake.invoked != 1 {
		t.Errorf("Lambda invoked %d times, want 1", fake.invoked)
	}
	if fake.lastIn.InvocationType != types.InvocationTypeEvent {
		t.Errorf("invocation type = %v, want Event (async)", fake.lastIn.InvocationType)
	}

	var got Event
	if err := json.Unmarshal(fake.lastIn.Payload, &got); err != nil {
		t.Fatalf("decode payload: %v", err)
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
	c := newTestClient(t, &fakeLambda{})
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
	c := newTestClient(t, &fakeLambda{})
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
	c := newTestClient(t, &fakeLambda{})
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
	c := newTestClient(t, &fakeLambda{})
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
			c := newTestClient(t, &fakeLambda{})
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
	c := newTestClient(t, &fakeLambda{})
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
	t.Parallel()
	fake := &fakeLambda{}
	c := newTestClient(t, fake)
	c.Declare("media", DeclFromOptions("infra.compute.ms", Price(0)))

	err := c.Record(context.Background(), "infra.compute.ms", 1)
	if err == nil || !strings.Contains(err.Error(), "platform-measured") {
		t.Errorf("expected a platform-measured rejection, got %v", err)
	}
	if fake.invoked != 0 {
		t.Errorf("reserved metric must not reach the transport; invoked=%d", fake.invoked)
	}
}

// TestRecord_NoKindOnWire asserts the §4 invariant: kind lives in the manifest,
// NOT on the wire. The serialized Event must carry no "kind" key (for either a
// counter or a gauge), the envelope version must stay 1, and the value is
// carried verbatim (V==1).
func TestRecord_NoKindOnWire(t *testing.T) {
	t.Parallel()
	for _, kindOpt := range []MetricOption{Counter, Gauge} {
		fake := &fakeLambda{}
		c := newTestClient(t, fake)
		c.Declare("store", DeclFromOptions("myapp.items", kindOpt))
		if err := c.Record(context.Background(), "myapp.items", 1); err != nil {
			t.Fatalf("Record: %v", err)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(fake.lastIn.Payload, &raw); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if _, ok := raw["kind"]; ok {
			t.Errorf("wire envelope must not carry a kind field, got keys %v", keys(raw))
		}
		var got Event
		if err := json.Unmarshal(fake.lastIn.Payload, &got); err != nil {
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

func TestRecord_PropagatesLambdaError(t *testing.T) {
	t.Parallel()
	fake := &fakeLambda{err: errors.New("throttled")}
	c := newTestClient(t, fake)
	declareCounter(t, c, "transcode.minutes")

	err := c.Record(context.Background(), "transcode.minutes", 1)
	if err == nil || !strings.Contains(err.Error(), "throttled") {
		t.Errorf("expected wrapped throttled error, got %v", err)
	}
}

// TestRecord_RejectsUndeclaredName asserts the declaration-first contract: a
// Record for a name never declared via ms.Meter returns an error and never
// reaches the transport.
func TestRecord_RejectsUndeclaredName(t *testing.T) {
	t.Parallel()
	fake := &fakeLambda{}
	c := newTestClient(t, fake)
	declareCounter(t, c, "transcode.minutes")

	err := c.Record(context.Background(), "never.declared", 1)
	if err == nil || !strings.Contains(err.Error(), "never declared") {
		t.Errorf("expected an undeclared-name error, got %v", err)
	}
	if fake.invoked != 0 {
		t.Errorf("undeclared metric must not reach the transport; invoked=%d", fake.invoked)
	}
}

func TestRecord_DevModeLogsToStderr(t *testing.T) {
	var buf bytes.Buffer
	c := NewDev(log.New(&buf, "", 0))
	c.Declare("media", DeclFromOptions("transcode.minutes", Counter))

	ctx := auth.Set(context.Background(), auth.Identity{AppID: "dev_app"})
	if err := c.Record(ctx, "transcode.minutes", 12); err != nil {
		t.Fatalf("Record: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, `appID="dev_app"`) ||
		!strings.Contains(out, `moduleID="media"`) ||
		!strings.Contains(out, `metric="transcode.minutes"`) ||
		!strings.Contains(out, `value=12`) {
		t.Errorf("unexpected log line: %q", out)
	}
	if strings.Contains(out, "kind=") {
		t.Errorf("dev log must not carry a kind (catalog is authoritative): %q", out)
	}
}

func TestRecord_NilLoggerDoesNotPanic(t *testing.T) {
	t.Parallel()
	// A zero Client{} (legal via the exported type, reachable in tests/mocks)
	// has both lambdaClient and logger nil. Record must take the dev-mode path
	// (lambdaClient == nil) and return without dereferencing the nil logger.
	c := &Client{}
	c.Declare("media", DeclFromOptions("transcode.minutes", Counter))

	ctx := auth.Set(context.Background(), auth.Identity{AppID: "dev_app"})
	if err := c.Record(ctx, "transcode.minutes", 7); err != nil {
		t.Fatalf("Record with nil logger should be a no-op, got: %v", err)
	}
}

func TestRecord_DevMode_EmptyAppIDWhenNoAuth(t *testing.T) {
	var buf bytes.Buffer
	c := NewDev(log.New(&buf, "", 0))
	c.Declare("media", DeclFromOptions("transcode.minutes", Counter))

	_ = c.Record(context.Background(), "transcode.minutes", 1)

	if !strings.Contains(buf.String(), `appID=""`) {
		t.Errorf("expected appID=\"\" when context has no auth identity, got: %q", buf.String())
	}
}

func TestRecord_RejectsNegativeAndNonFinite(t *testing.T) {
	t.Parallel()
	fake := &fakeLambda{}
	c := newTestClient(t, fake)
	declareCounter(t, c, "transcode.minutes")

	bad := []float64{-1, math.NaN(), math.Inf(1), math.Inf(-1)}
	for _, v := range bad {
		if err := c.Record(context.Background(), "transcode.minutes", v); err == nil {
			t.Errorf("Record(%g) should return an error (finite, non-negative)", v)
		}
	}
	if fake.invoked != 0 {
		t.Errorf("invalid values must not reach the transport; invoked=%d", fake.invoked)
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
			c := newTestClient(t, &fakeLambda{})
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
			c := newTestClient(t, &fakeLambda{})
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
	c := newTestClient(t, &fakeLambda{})
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
	c := newTestClient(t, &fakeLambda{})
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
	t.Parallel()
	fake := &fakeLambda{}
	c := newTestClient(t, fake)
	declareCounter(t, c, "transcode.minutes")

	if err := c.Record(context.Background(), "transcode.minutes", 1); err != nil {
		t.Fatalf("Record: %v", err)
	}
	var first Event
	if err := json.Unmarshal(fake.lastIn.Payload, &first); err != nil {
		t.Fatalf("decode first payload: %v", err)
	}
	if first.EventID == "" {
		t.Fatal("EventID should be set")
	}

	// Retry of the SAME logical call must reuse the SAME EventID. dispatch is
	// the per-attempt transport leg; Record owns the minted event.
	if err := c.dispatch(context.Background(), first); err != nil {
		t.Fatalf("dispatch retry: %v", err)
	}
	var retried Event
	if err := json.Unmarshal(fake.lastIn.Payload, &retried); err != nil {
		t.Fatalf("decode retry payload: %v", err)
	}
	if retried.EventID != first.EventID {
		t.Errorf("EventID changed across retry: first=%q retry=%q (ON CONFLICT cannot dedupe)", first.EventID, retried.EventID)
	}
}
