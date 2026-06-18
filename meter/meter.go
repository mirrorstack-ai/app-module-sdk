// Package meter is the module usage-metering surface. A module DECLARES each
// metric once, up front, with its kind + unit + price; runtime code then emits
// BY NAME with a single Record call — exactly mirroring ms.Emits (declare) /
// ms.Emit (emit by name). Declaration registers the metric into the module
// MANIFEST (via the registry, in core) AND into the module's meter registry, so
// the platform can populate its metric catalog BEFORE any event arrives — and a
// call site can never mislabel a metric's kind, because kind is read from the
// catalog, not the wire. There is NO stored handle: Record resolves the metric
// by name and fails fast if it was never declared (declaration-first).
//
// Security model: the SDK runs inside the module's own (untrusted) process.
// Wire-format fields suffixed with Hint (AppIDHint, ModuleIDHint,
// RecordedAtHint) are NOT trusted — the platform re-derives authoritative
// values from the authenticated invoker. Reported VALUES affect only the
// developer's own customer billing. The reserved infra.*/platform.* namespace
// is rejected at declaration (and at platform ingress) so a module cannot
// declare or self-report a platform-billable infra metric.
package meter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/internal/ids"
)

// devDispatchFallback is the platform-dispatch base used when MS_DISPATCH_URL is
// unset. Modules run inside docker; the dispatch listens on the host at :8083,
// reachable via host.docker.internal — the same convention ms.Call / ms.Emit
// use (core.devDispatchFallback). The CLI sets MS_DISPATCH_URL explicitly in
// dev and prod points it at the real dispatch; this default only keeps local
// runs working. Restated here (not imported) because meter is a public package
// and must not import internal/core.
const devDispatchFallback = "http://host.docker.internal:8083"

// meterTimeout bounds a single usage-POST to the dispatch. Matches the
// inter-module call timeout (core.callTimeout); metering is best-effort and must
// never hang a handler.
const meterTimeout = 15 * time.Second

// reservedPrefixes are the platform-owned metric namespaces. A module may not
// declare or self-report a metric under these — they belong to platform-side
// infra metering (model tokens, storage bytes, egress, compute), which is
// measured at the platform chokepoint and never trusts an SDK value. ms.Meter
// panics on a reserved name, and the platform ingress rejects it too.
var reservedPrefixes = []string{"infra.", "platform."}

// Kind is the billing semantic of a declared metric. It is fixed at
// declaration and recorded in the manifest; it does NOT travel on the wire
// (the platform's manifest-fed catalog is authoritative). A single Record
// call emits for both kinds — the kind decides how the platform aggregates.
//
// Kind is set via the Counter / Gauge declaration OPTIONS, not as a positional
// argument; counterKind / gaugeKind are the internal enum values those options
// carry into the manifest and registry.
type Kind string

const (
	// counterKind is an additive / one-time value the platform SUMs over the
	// billing period (orders placed, minutes transcoded, messages sent). Set
	// via the Counter option.
	counterKind Kind = "counter"
	// gaugeKind is an absolute current level the platform never SUMs — it
	// takes the MAX or a time-weighted integral (stored bytes, active rows,
	// open connections). Set via the Gauge option.
	gaugeKind Kind = "gauge"
)

// IsValid reports whether k is one of the two known kinds.
func (k Kind) IsValid() bool { return k == counterKind || k == gaugeKind }

// MetricOption configures a metric at declaration time. The KIND is itself an
// option (Counter / Gauge), alongside Unit and Price. A custom metric MUST pass
// exactly one kind option; a reserved infra.*/platform.* metric may pass ONLY
// Price (its kind/unit are platform-owned). All other combinations panic at
// declaration (see Declare).
//
// MetricOption is an INTERFACE, not a func type, on purpose: it lets Counter and
// Gauge be untyped-style package CONSTANTS rather than reassignable package
// vars. The SDK is a security boundary — if Counter/Gauge were exported `var`s a
// hostile module could execute `ms.Counter = nil` and silently break every
// later Meter call. As const-backed interface values they cannot be reassigned
// from outside (or inside) the package, while the call site stays parens-free
// (`ms.Meter(name, ms.Counter, ...)`).
type MetricOption interface {
	applyMetric(*metricOptions)
}

type metricOptions struct {
	kind     Kind
	kindSet  bool
	kindDup  bool // a second, conflicting kind option was passed
	unit     string
	unitSet  bool
	price    int64
	priceSet bool
}

// Counter / Gauge are CONSTANTS, not vars, so a third-party module cannot
// reassign them (the SDK is a trust boundary). A const of a defined type is
// immutable from any package; reassigning `ms.Counter = …` is a compile error.
const (
	// Counter declares the metric as additive: the platform SUMs it over the
	// billing period. Use it for things you count: orders placed, minutes
	// transcoded, messages sent. Pass it to Meter as the kind option.
	Counter kindOption = kindOption(counterKind)
	// Gauge declares the metric as an absolute current level the platform
	// never SUMs — it takes the MAX or a time-weighted integral. Use it for
	// levels you can safely re-report on a heartbeat: stored bytes, active
	// rows, open connections. Gauge is self-healing: a lost sample only loses
	// resolution. Pass it to Meter as the kind option.
	Gauge kindOption = kindOption(gaugeKind)
)

// kindOption is the immutable MetricOption type backing the Counter / Gauge
// CONSTANTS. Being a defined type over Kind, its values are const-able (so they
// cannot be reassigned), unlike a func-typed option.
type kindOption Kind

// applyMetric records the kind on the accumulating options. If a kind was
// already set to a different value, it records the conflict so Declare can panic
// (a metric cannot be both a counter and a gauge).
func (k kindOption) applyMetric(o *metricOptions) {
	if o.kindSet && o.kind != Kind(k) {
		o.kindDup = true
	}
	o.kind = Kind(k)
	o.kindSet = true
}

// metricOptionFunc adapts a closure to MetricOption for the non-kind options
// (Unit / Price), which carry runtime values and so cannot be constants.
type metricOptionFunc func(*metricOptions)

func (f metricOptionFunc) applyMetric(o *metricOptions) { f(o) }

// Unit sets the metric's display unit (e.g. "order", "byte"). Informational
// metadata for the platform UI / invoice line; it does not affect aggregation.
func Unit(u string) MetricOption {
	return metricOptionFunc(func(o *metricOptions) {
		o.unit = u
		o.unitSet = true
	})
}

// Price sets the metric's per-unit CUSTOMER price in micro-dollars (1e-6 USD).
// This is the developer's Plane-2 pricing for THEIR customer; the platform
// charges quantity × this price with NO blanket markup (the flat 1.2× applies
// only to platform-infra metrics, never a module's custom metric). Optional —
// omit it to meter without charging.
func Price(microDollars int64) MetricOption {
	return metricOptionFunc(func(o *metricOptions) {
		o.price = microDollars
		o.priceSet = true
	})
}

// DeclFromOptions applies the variadic options to produce a Decl. Used by core
// (ms.Meter) to translate the public name + options into the declaration that
// is both validated + registered (Declare) and registered into the manifest
// (registry.AddMetric). The kind is itself an option (Counter / Gauge); the
// resulting Decl records which option groups were set so Declare can enforce
// the custom-vs-reserved rules (custom requires a kind; reserved allows price
// only).
func DeclFromOptions(name string, opts ...MetricOption) Decl {
	o := &metricOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt.applyMetric(o)
		}
	}
	return Decl{
		Name:     name,
		Kind:     o.kind,
		KindSet:  o.kindSet,
		kindDup:  o.kindDup,
		Unit:     o.unit,
		UnitSet:  o.unitSet,
		Price:    o.price,
		PriceSet: o.priceSet,
	}
}

// Decl is the declared shape of a metric: name + kind + unit + optional price.
// It is what flows into the manifest (see registry.MetricDecl), so the platform
// can populate its metric_definitions catalog at install/publish.
//
// KindSet / UnitSet / PriceSet report whether each option group was supplied,
// so Declare can enforce that a custom metric carries a kind and a reserved
// infra.*/platform.* metric carries price only. For a reserved price-override
// declaration Kind is empty (KindSet false) and the platform catalog supplies
// the kind/unit.
//
// Construct a Decl via DeclFromOptions, NOT a struct literal: it has an
// unexported conflict-tracking field (kindDup) that a literal cannot set, so a
// hand-built Decl would silently skip the conflicting-kind guard.
type Decl struct {
	Name     string
	Kind     Kind
	KindSet  bool
	kindDup  bool // both Counter and Gauge were passed (conflicting kinds)
	Unit     string
	UnitSet  bool
	Price    int64
	PriceSet bool
}

// IsReserved reports whether name falls under a platform-owned namespace
// (infra.*/platform.*). A reserved metric is platform-measured: a module may
// declare it with Price ONLY (to override the customer passthrough) but may
// never set its kind/unit or self-report its value.
func IsReserved(name string) bool {
	for _, p := range reservedPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// ValidateMetricName rejects names that are empty, contain a path separator,
// whitespace, a dot-segment, or a null byte. It mirrors registry.ValidateName.
// The mirroring is deliberate, NOT accidental duplication: meter is a public
// package and must not import internal/registry, so the shared rule is restated
// here rather than shared via an internal import.
//
// The reserved infra.*/platform.* namespace is NOT rejected here: a module may
// declare such a name with Price ONLY to override the customer passthrough. The
// reserved-vs-custom option rules are enforced in Declare; self-reporting a
// reserved name is rejected in Record.
func ValidateMetricName(name string) {
	if name == "" {
		panic("mirrorstack/meter: Meter name cannot be empty")
	}
	if strings.ContainsAny(name, "/\\ \t\n\r\x00") {
		panic("mirrorstack/meter: Meter(" + name + ") contains a path separator, whitespace, or null byte")
	}
	if strings.Contains(name, "..") {
		panic("mirrorstack/meter: Meter(" + name + ") contains '..'")
	}
}

// Declare validates a metric declaration and registers it into the client's
// metric registry under decl.Name, so a later Record(ctx, name, value)
// resolves it BY NAME (mirroring how ms.Emits records an emit name that
// ms.Emit later resolves). NO handle is returned — emission is by name.
//
// The caller (core) is responsible for registering decl into the MANIFEST
// (registry.AddMetric); Declare owns the name/kind/reserved-prefix validation
// so the public ms.Meter contract is enforced in one place, plus its own
// registry duplicate guard.
//
// Validation (all panic — declaration is startup code, so a bad declaration is
// a programmer error that must fail loudly):
//   - empty/malformed name;
//   - conflicting kinds (both Counter and Gauge passed);
//   - a CUSTOM (non-reserved) name without exactly one kind option;
//   - a RESERVED infra.*/platform.* name carrying a kind or unit option
//     (those are platform-owned — a reserved name may carry Price ONLY, to
//     override the customer passthrough);
//   - a duplicate metric name (a second declaration would silently disagree
//     on kind/price).
func (c *Client) Declare(moduleID string, decl Decl) {
	ValidateMetricName(decl.Name)
	if decl.kindDup {
		panic(fmt.Sprintf("mirrorstack/meter: Meter(%q) was given both ms.Counter and ms.Gauge (a metric has exactly one kind)", decl.Name))
	}
	if IsReserved(decl.Name) {
		// Reserved infra.*/platform.* metrics are platform-measured: the
		// platform owns kind/unit/measurement. A module may declare one with
		// Price ONLY (a Plane-2 customer-passthrough override). Any kind or
		// unit option on a reserved name is a programmer error.
		if decl.KindSet {
			panic(fmt.Sprintf("mirrorstack/meter: Meter(%q) is a reserved platform metric — it may carry ms.Price only; kind is platform-owned (drop ms.Counter/ms.Gauge)", decl.Name))
		}
		if decl.UnitSet {
			panic(fmt.Sprintf("mirrorstack/meter: Meter(%q) is a reserved platform metric — it may carry ms.Price only; unit is platform-owned (drop ms.Unit)", decl.Name))
		}
		// A reserved declaration's ONLY purpose is to override the customer
		// passthrough price. With no Price it is a no-op that would otherwise
		// pollute the manifest with a meaningless empty entry — reject it.
		if !decl.PriceSet {
			panic(fmt.Sprintf("mirrorstack/meter: Meter(%q) is a reserved platform metric — pass ms.Price to override the customer passthrough, or remove this declaration", decl.Name))
		}
	} else {
		// Custom metric: a kind is mandatory so the platform knows SUM vs
		// MAX/integral, and a call site can never mislabel it.
		if !decl.KindSet {
			panic(fmt.Sprintf("mirrorstack/meter: Meter(%q) needs a kind — pass ms.Counter or ms.Gauge", decl.Name))
		}
		if !decl.Kind.IsValid() {
			panic(fmt.Sprintf("mirrorstack/meter: Meter(%q) has invalid kind %q (use ms.Counter or ms.Gauge)", decl.Name, decl.Kind))
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.moduleID == "" {
		c.moduleID = moduleID
	}
	if c.metrics == nil {
		c.metrics = make(map[string]Decl)
	}
	if _, dup := c.metrics[decl.Name]; dup {
		panic("mirrorstack/meter: Meter(" + decl.Name + ") declared twice")
	}
	c.metrics[decl.Name] = decl
}

// Record emits a usage event for the metric declared under name with the given
// value. It mirrors ms.Emit: resolve the declared name, build the envelope,
// hand it to the transport. The platform reads the declared kind from its
// manifest-fed catalog to decide SUM vs MAX/integral, so a call site can never
// mislabel a metric.
//
// Declaration-first: if name was never declared via ms.Meter, Record returns an
// error (fail fast in dev) — it never silently emits an unknown metric.
//
// Emitted by POSTing the Event envelope to the platform dispatch usage ingress
// ({MS_DISPATCH_URL | dev fallback}/apps/{appID}/usage), exactly mirroring
// ms.Emit. Dispatch re-derives the authoritative app/module/owner/recorded_at
// and forwards to billing-engine; the SDK's Hint fields are untrusted. An empty
// app id (no auth identity in ctx) is an error (no panic), like ms.Emit.
//
// Call sparingly — once per meaningful action, not per row processed. Errors
// are returned, NOT panicked, and should be logged, not propagated: a billing
// failure must never fail the handler.
//
// Returns an error (does NOT panic) if value is negative, NaN, or infinite —
// a single bad value can't crash the handler. The metric name was already
// validated at declaration.
//
// The EventID is minted ONCE per Record call (and the built Event reused across
// any transport retry of the same call) so the platform's ON CONFLICT(event_id)
// dedupe holds and a retried delivery is not double-counted.
func (c *Client) Record(ctx context.Context, name string, value float64) error {
	c.mu.RLock()
	decl, declared := c.metrics[name]
	moduleID := c.moduleID
	c.mu.RUnlock()
	if IsReserved(name) {
		// A reserved infra.*/platform.* metric is platform-measured. A module
		// may DECLARE it (Price-only, to override the customer passthrough) but
		// may never self-report its value — the platform meters it at its own
		// chokepoint, and an SDK-reported quantity for it is never billable.
		return fmt.Errorf("mirrorstack/meter: metric %q is platform-measured and cannot be self-reported via ms.Record (it may be declared with ms.Price only)", name)
	}
	if !declared {
		return fmt.Errorf("mirrorstack/meter: metric %q was never declared (call ms.Meter(%q, ...) in setup before ms.Record)", name, name)
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return fmt.Errorf("mirrorstack/meter: metric %q: value must be finite and non-negative, got %g", decl.Name, value)
	}

	appID := ""
	if a := auth.Get(ctx); a != nil {
		appID = a.AppID
	}
	// An empty app id means there is no app scope to attribute the usage to.
	// Mirror ms.Emit: return an error (no panic), and do not reach the transport.
	if appID == "" {
		return fmt.Errorf("mirrorstack/meter: Record requires an app-scoped context (no AppID in auth identity)")
	}

	// Mint the EventID ONCE here so a retried delivery reuses it and the
	// platform's ON CONFLICT(event_id) dedupe holds. NO kind on the wire — the
	// manifest/catalog is authoritative.
	event := Event{
		V:              envelopeVersion,
		EventID:        ids.NewUUID(),
		AppIDHint:      appID,
		ModuleIDHint:   moduleID,
		Metric:         decl.Name,
		Value:          value,
		RecordedAtHint: time.Now().UTC(),
	}
	return c.dispatch(ctx, appID, event)
}

// resolveUsageURL builds the platform-dispatch usage-ingress URL the Event is
// POSTed to:
//
//	{base}/apps/{appID}/usage
//
// base is MS_DISPATCH_URL (the container->dispatch base) with the
// host.docker.internal:8083 dev fallback when unset — the same resolution
// ms.Call / ms.Emit use. Dispatch re-derives the authoritative app/module from
// the authenticated invoker; the appID in the path is the SDK's hint scope.
//
// DEV/DISPATCH TRANSPORT. The prod module->dispatch leg rides task #146 (the
// same seam ms.Emit's resolveEventBusURL documents); the dispatch->billing leg
// is already prod-ready. When #146 lands, swap the body of this function (and
// only this function) to consult the prod transport; Record's
// marshal/auth/error contract stays put.
func resolveUsageURL(appID string) string {
	base := os.Getenv("MS_DISPATCH_URL")
	if base == "" {
		base = devDispatchFallback
	}
	base = strings.TrimRight(base, "/")
	return fmt.Sprintf("%s/apps/%s/usage", base, appID)
}

// dispatch delivers an already-built Event to the platform dispatch usage
// ingress over HTTP, mirroring ms.Emit. The caller (Record) mints the EventID
// once, so retrying dispatch with the same Event reuses the same EventID and the
// platform deduplicates rather than double-counts. A non-2xx response is an
// error with the body truncated to ~2 KB; the non-fatal contract (log, don't
// propagate) is the caller's responsibility.
func (c *Client) dispatch(ctx context.Context, appID string, event Event) error {
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("mirrorstack/meter: marshal event: %w", err)
	}

	u := resolveUsageURL(appID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-MS-App-ID", appID)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("ms.Record %s -> %d: %s", req.URL.Path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// Client is the module-level meter transport AND metric registry. Created
// eagerly at ms.Init: it POSTs each usage Event to the platform dispatch usage
// ingress over HTTP (mirroring ms.Emit / ms.Call), in both dev and prod — there
// is no separate dev sink. Declared metrics live in its registry, keyed by name,
// so Record resolves a metric by name (mirroring ms.Emits/ms.Emit).
//
// The transport is dispatch-HTTP: MS_DISPATCH_URL (or the dev fallback) routes
// to the dispatch usage ingress, which re-derives the authoritative attribution
// and forwards to billing-engine. The HTTP client is ALWAYS built (never nil) —
// in dev it harmlessly targets the local dispatch, and a transport failure is
// non-fatal (returned, then logged by the caller, not propagated).
type Client struct {
	httpClient *http.Client

	mu       sync.RWMutex
	moduleID string          // emitting module's Config.ID, set at first Declare
	metrics  map[string]Decl // declared metrics, keyed by name (Record resolves here)
}

// New creates a meter client. The transport is dispatch-HTTP in both dev and
// prod (the URL is resolved per-Record from MS_DISPATCH_URL with the dev
// fallback), so there is a single constructor — no ARN/dev split. The HTTP
// client is always built so Record never dereferences a nil transport.
//
// Fail-fast prod-base validation mirrors ms.Emit: if MS_DISPATCH_URL is set it
// must be a parseable http(s) URL, so a typo fails at startup rather than at the
// first (silently lost) Record call. An unset MS_DISPATCH_URL is valid (dev
// fallback).
func New() (*Client, error) {
	if base := os.Getenv("MS_DISPATCH_URL"); base != "" {
		if u, err := url.Parse(base); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("mirrorstack/meter: MS_DISPATCH_URL %q is not a valid http(s) base URL", base)
		}
	}
	return &Client{httpClient: &http.Client{Timeout: meterTimeout}}, nil
}
