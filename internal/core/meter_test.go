package core

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/meter"
	"github.com/mirrorstack-ai/app-module-sdk/system"
)

// TestMeter_DeclaresKindAndPriceIntoManifest asserts that ms.Meter records the
// declared name + kind + unit + price into the manifest's metrics[] (the path
// the platform reads to populate its metric_definitions catalog). Mirrors the
// Emits/Permissions manifest tests.
func TestMeter_DeclaresKindAndPriceIntoManifest(t *testing.T) {
	m := newTestModuleWithSecret(t, "media")

	m.Meter("orders.placed", meter.Counter, meter.Unit("order"), meter.Price(50_000))
	m.Meter("myapp.objects.bytes", meter.Gauge, meter.Unit("byte")) // no price

	rec := doRequestWithSecret(t, m.Router(), "GET", "/__mirrorstack/platform/manifest", "secret")
	var got system.ManifestPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	if len(got.Metrics) != 2 {
		t.Fatalf("metrics = %d, want 2: %+v", len(got.Metrics), got.Metrics)
	}
	for _, d := range got.Metrics {
		switch d.Name {
		case "orders.placed":
			if d.Kind != "counter" || d.Unit != "order" {
				t.Errorf("orders.placed = %+v, want kind=counter unit=order", d)
			}
			if d.Price == nil || *d.Price != 50_000 {
				t.Errorf("orders.placed price = %v, want 50000", d.Price)
			}
		case "myapp.objects.bytes":
			if d.Kind != "gauge" || d.Unit != "byte" {
				t.Errorf("myapp.objects.bytes = %+v, want kind=gauge unit=byte", d)
			}
			if d.Price != nil {
				t.Errorf("myapp.objects.bytes price = %v, want nil (no price declared)", d.Price)
			}
		default:
			t.Errorf("unexpected metric %q", d.Name)
		}
	}
}

// TestMeter_PriceZeroIsDistinctFromUnpriced asserts a declared price of 0 is
// carried (PriceSet), distinct from omitting Price entirely.
func TestMeter_PriceZeroIsDistinctFromUnpriced(t *testing.T) {
	m := newTestModuleWithSecret(t, "media")
	m.Meter("free.metric", meter.Counter, meter.Price(0))

	rec := doRequestWithSecret(t, m.Router(), "GET", "/__mirrorstack/platform/manifest", "secret")
	var got system.ManifestPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if len(got.Metrics) != 1 {
		t.Fatalf("metrics = %d, want 1", len(got.Metrics))
	}
	if got.Metrics[0].Price == nil || *got.Metrics[0].Price != 0 {
		t.Errorf("price = %v, want explicit 0", got.Metrics[0].Price)
	}
}

func TestMeter_PanicsOnDuplicateName(t *testing.T) {
	m, _ := New(Config{ID: "media"})
	m.Meter("orders.placed", meter.Counter)

	assertPanics(t, "expected panic on duplicate Meter name", func() {
		m.Meter("orders.placed", meter.Gauge)
	})
}

func TestMeter_PanicsOnReservedPrefix(t *testing.T) {
	m, _ := New(Config{ID: "media"})
	for _, name := range []string{"infra.compute.ms", "platform.storage.bytes"} {
		assertPanics(t, "expected panic on reserved-prefix Meter "+name, func() {
			m.Meter(name, meter.Counter)
		})
	}
}

func TestMeter_TopLevelPanicsBeforeInit(t *testing.T) {
	resetDefault(t)
	assertPanics(t, "expected panic for top-level Meter before Init", func() {
		Meter("orders.placed", meter.Counter)
	})
}

// TestRecord_ResolvesDeclaredByName asserts the by-name runtime emit: ms.Record
// for a DECLARED metric succeeds (dev client logs, returns nil), mirroring
// ms.Emit's emit-by-name shape.
func TestRecord_ResolvesDeclaredByName(t *testing.T) {
	m, _ := New(Config{ID: "media"})
	m.Meter("orders.placed", meter.Counter, meter.Unit("order"))

	if err := m.Record(context.Background(), "orders.placed", 1); err != nil {
		t.Fatalf("Record of a declared metric: %v", err)
	}
}

// TestRecord_RejectsUndeclaredName asserts declaration-first: ms.Record for a
// name never declared via ms.Meter returns an error (no silent emit).
func TestRecord_RejectsUndeclaredName(t *testing.T) {
	m, _ := New(Config{ID: "media"})
	m.Meter("orders.placed", meter.Counter)

	if err := m.Record(context.Background(), "never.declared", 1); err == nil {
		t.Error("expected an error recording an undeclared metric name")
	}
}
