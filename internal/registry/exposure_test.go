package registry

import (
	"slices"
	"testing"
)

func TestAddExposedTable_Records(t *testing.T) {
	t.Parallel()

	r := New()
	r.AddExposedTable("orders")
	r.AddExposedTable("invoices")

	got := r.ExposedTables()
	if !slices.Equal(got, []string{"invoices", "orders"}) {
		t.Errorf("ExposedTables() = %v, want sorted [invoices orders]", got)
	}
}

func TestExposedTables_UnionDedup(t *testing.T) {
	t.Parallel()

	// Re-declaring the same name (e.g. behind a feature flag) is a no-op:
	// the set union keeps each name once.
	r := New()
	r.AddExposedTable("orders")
	r.AddExposedTable("orders")
	r.AddExposedTable("invoices")

	got := r.ExposedTables()
	if len(got) != 2 {
		t.Errorf("ExposedTables() = %v, want 2 distinct names", got)
	}
}

func TestExposedTables_SortedDeterministic(t *testing.T) {
	t.Parallel()

	// Declaration order must NOT affect output order — the manifest must be
	// stable for prompt-cache / manifest-diffing. Both orderings sort the same.
	a := New()
	a.AddExposedTable("zebra")
	a.AddExposedTable("apple")
	a.AddExposedTable("mango")

	b := New()
	b.AddExposedTable("mango")
	b.AddExposedTable("zebra")
	b.AddExposedTable("apple")

	want := []string{"apple", "mango", "zebra"}
	if got := a.ExposedTables(); !slices.Equal(got, want) {
		t.Errorf("a.ExposedTables() = %v, want %v", got, want)
	}
	if got := b.ExposedTables(); !slices.Equal(got, want) {
		t.Errorf("b.ExposedTables() = %v, want %v (order-independent)", got, want)
	}
}

func TestExposedTables_EmptyReturnsNonNil(t *testing.T) {
	t.Parallel()
	if got := New().ExposedTables(); got == nil {
		t.Error("empty ExposedTables() returned nil, want []string{}")
	}
}

func TestExposedTables_ReturnsCopy(t *testing.T) {
	t.Parallel()

	r := New()
	r.AddExposedTable("orders")

	first := r.ExposedTables()
	first[0] = "tampered"

	if second := r.ExposedTables(); second[0] != "orders" {
		t.Errorf("ExposedTables() returned shared backing slice: caller mutation leaked, got %v", second)
	}
}

func TestAddExposedTable_PanicsOnInvalidName(t *testing.T) {
	t.Parallel()

	bad := []string{
		"",             // empty
		"Orders",       // uppercase
		"1orders",      // leading digit
		"my orders",    // whitespace
		"orders-table", // hyphen (not a Postgres-safe bare identifier)
		"mod.orders",   // dot / schema-qualified
		"../etc",       // path traversal shape
	}
	for _, name := range bad {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				if rec := recover(); rec == nil {
					t.Errorf("AddExposedTable(%q) did not panic, want panic on invalid identifier", name)
				}
			}()
			New().AddExposedTable(name)
		})
	}
}

func TestAddExposedTable_AcceptsValidNames(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"orders", "order_items", "o", "a1", "user_2fa_tokens"} {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				if rec := recover(); rec != nil {
					t.Errorf("AddExposedTable(%q) panicked, want accept: %v", name, rec)
				}
			}()
			New().AddExposedTable(name)
		})
	}
}
