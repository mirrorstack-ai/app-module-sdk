package main

// CLI flag: --use-meter
// Remove this file if the module doesn't emit billable usage events.
//
// DECLARE each metric ONCE, up front, with its kind + unit + price; then emit
// at runtime BY NAME with a single ms.Record(ctx, name, value) — exactly
// mirroring ms.Emits (declare) / ms.Emit (emit by name). Declaration registers
// the metric into the manifest, so the platform's catalog knows how to
// aggregate and price it before any event arrives; there is no handle to keep.
//
// Use ms.Counter for additive counts (orders placed, minutes transcoded) and
// ms.Gauge for an absolute current level you re-report on a heartbeat (your own
// external store size, active rows). Billing errors are logged but must not fail
// the handler.
//
// Gauge metric names must be module-owned (e.g. "myapp.objects.bytes"). The
// platform measures its own infra and reserves the infra.*/platform.* namespace:
// you may declare a reserved metric with ms.Price ONLY to override what your
// customer is billed for that infra (passing a kind or unit on it panics), but
// you can never self-report its value via ms.Record — the platform meters it.

import (
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	ms "github.com/mirrorstack-ai/app-module-sdk"
)

func init() {
	postInitHooks = append(postInitHooks, registerMeter)
}

func registerMeter() {
	// Counter: an additive business metric priced at $0.05/order (50_000
	// micro-dollars). The platform SUMs counters over the billing period.
	ms.Meter("orders.placed", ms.Counter, ms.Unit("order"), ms.Price(50_000))

	// Gauge: an absolute current level of a MODULE-OWNED metric (here, total
	// bytes in the module's own external store). ms.Record reports the CURRENT
	// absolute level (not a delta); the platform never sums a gauge — it takes
	// the MAX or a time-weighted integral over the billing period, so the price
	// is charged per aggregated byte-hour / peak (the platform's rollup choice),
	// NOT once per reported sample. Re-report on a heartbeat (design §7). The
	// price here is illustrative — a real byte gauge with a per-byte price can
	// produce large invoices, so pick the cadence + price deliberately.
	ms.Meter("myapp.objects.bytes", ms.Gauge, ms.Unit("byte"), ms.Price(1))

	// Reserved infra metric: PRICE-OVERRIDE only. Here we set the per-unit
	// customer passthrough for platform compute to 0 — absorbing platform
	// compute into our own pricing (we still owe the platform the measured COGS
	// regardless; this only changes what OUR customer is billed). kind/unit are
	// platform-owned, so we pass ms.Price alone — adding ms.Counter/ms.Gauge or
	// ms.Unit here would panic, and ms.Record("infra.compute.ms", ...) is
	// rejected (the platform meters compute at its own chokepoint).
	ms.Meter("infra.compute.ms", ms.Price(0))

	ms.Platform(func(r chi.Router) {
		r.Post("/orders", func(w http.ResponseWriter, r *http.Request) {
			// ... place the order ...

			// Emit BY NAME — the metric was declared above. The platform reads
			// the declared kind from its catalog, so the call site never repeats it.
			if err := ms.Record(r.Context(), "orders.placed", 1); err != nil {
				log.Printf("meter: %v", err) // don't fail the handler
			}

			// Report the current absolute level of the module's own store.
			currentBytes := 4096.0
			if err := ms.Record(r.Context(), "myapp.objects.bytes", currentBytes); err != nil {
				log.Printf("meter: %v", err) // don't fail the handler
			}
			w.WriteHeader(http.StatusOK)
		})
	})
}
