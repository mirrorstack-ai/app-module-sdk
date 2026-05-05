package registry

import (
	"strings"
	"testing"
)

func TestAddExposure_StoresAndReturns(t *testing.T) {
	r := New()
	r.AddExposure("recent_orders")
	out := r.Exposures()
	if len(out) != 1 || out[0].Name != "recent_orders" {
		t.Fatalf("unexpected exposures: %+v", out)
	}
}

func TestAddExposure_FirstWinsOnDuplicate(t *testing.T) {
	r := New()
	r.AddExposure("orders")
	r.AddExposure("orders")
	if got := r.Exposures(); len(got) != 1 {
		t.Errorf("expected dedup to keep one entry, got %d: %+v", len(got), got)
	}
}

func TestAddExposure_PreservesOrderAcrossDifferentNames(t *testing.T) {
	r := New()
	r.AddExposure("a")
	r.AddExposure("b")
	r.AddExposure("c")
	out := r.Exposures()
	if len(out) != 3 || out[0].Name != "a" || out[1].Name != "b" || out[2].Name != "c" {
		t.Errorf("registration-order broken: %+v", out)
	}
}

func TestAddExposure_RejectsBadName(t *testing.T) {
	bad := []string{
		"",           // empty (caught by ValidateName)
		"Recent",     // uppercase
		"1items",     // starts with digit
		"my-table",   // hyphen
		"with space", // whitespace (caught by ValidateName)
		"path/sep",   // slash (caught by ValidateName)
	}
	for _, name := range bad {
		assertExposurePanics(t, "AddExposure("+name+") should panic", func() {
			New().AddExposure(name)
		})
	}
}

func TestAddExposure_NameLengthBoundary(t *testing.T) {
	max := "a" + strings.Repeat("b", 62) // 63 = NAMEDATALEN
	r := New()
	r.AddExposure(max)
	if r.Exposures()[0].Name != max {
		t.Error("63-char name should accept")
	}

	overflow := "a" + strings.Repeat("b", 63) // 64
	assertExposurePanics(t, "64-char name should panic", func() {
		New().AddExposure(overflow)
	})
}

func TestExposures_EmptyReturnsNonNilArray(t *testing.T) {
	// Manifest contract: empty exposures must serialize as `[]`, not `null`.
	if got := New().Exposures(); got == nil {
		t.Error("expected non-nil empty slice, got nil")
	}
}

func assertExposurePanics(t *testing.T, msg string, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r == nil {
			t.Error(msg)
		}
	}()
	fn()
}
