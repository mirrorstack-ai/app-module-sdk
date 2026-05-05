package registry

import (
	"slices"
	"testing"
)

func TestAddExposure_StoresAndReturns(t *testing.T) {
	r := New()
	r.AddExposure(Exposure{
		Name:       "recent_orders",
		Kind:       ExposureKindView,
		ReadableBy: []string{"@*/analytics"},
	})
	out := r.Exposures()
	if len(out) != 1 || out[0].Name != "recent_orders" || out[0].Kind != ExposureKindView {
		t.Fatalf("unexpected exposures: %+v", out)
	}
	if !slices.Equal(out[0].ReadableBy, []string{"@*/analytics"}) {
		t.Errorf("ReadableBy round-trip lost: %+v", out[0].ReadableBy)
	}
}

func TestAddExposure_LastWinsOnSameName(t *testing.T) {
	r := New()
	r.AddExposure(Exposure{Name: "links", Kind: ExposureKindView, ReadableBy: []string{"@me/oauth-google"}})
	r.AddExposure(Exposure{Name: "links", Kind: ExposureKindView, ReadableBy: []string{"@*/oauth-*"}})
	out := r.Exposures()
	if len(out) != 1 {
		t.Fatalf("expected dedup, got %d", len(out))
	}
	if !slices.Equal(out[0].ReadableBy, []string{"@*/oauth-*"}) {
		t.Errorf("expected last-wins, got %+v", out[0].ReadableBy)
	}
}

func TestAddExposure_PreservesOrderAcrossDifferentNames(t *testing.T) {
	r := New()
	r.AddExposure(Exposure{Name: "a", Kind: ExposureKindView})
	r.AddExposure(Exposure{Name: "b", Kind: ExposureKindView})
	r.AddExposure(Exposure{Name: "c", Kind: ExposureKindTable})
	out := r.Exposures()
	if len(out) != 3 || out[0].Name != "a" || out[1].Name != "b" || out[2].Name != "c" {
		t.Errorf("registration-order broken: %+v", out)
	}
}

func TestExposures_DefensiveCopy(t *testing.T) {
	r := New()
	r.AddExposure(Exposure{
		Name:       "recent",
		Kind:       ExposureKindView,
		ReadableBy: []string{"@*/analytics"},
	})
	clone := r.Exposures()
	clone[0].ReadableBy[0] = "@*/MUTATED"
	again := r.Exposures()
	if again[0].ReadableBy[0] != "@*/analytics" {
		t.Errorf("registry mutated through returned clone: %+v", again[0])
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
			New().AddExposure(Exposure{Name: name, Kind: ExposureKindView})
		})
	}
}

func TestAddExposure_RejectsBadKind(t *testing.T) {
	assertExposurePanics(t, "unknown kind should panic", func() {
		New().AddExposure(Exposure{Name: "ok", Kind: "matview"})
	})
}

func TestAddExposure_RejectsBadReader(t *testing.T) {
	bad := []string{
		"me/foo",      // missing @
		"@me",         // missing slash
		"@me/",        // empty module
		"@/foo",       // empty owner
		"@me/foo bar", // whitespace
		"@me//foo",    // empty middle
		"@Me/foo",     // uppercase
	}
	for _, reader := range bad {
		assertExposurePanics(t, "AddExposure readableBy "+reader+" should panic", func() {
			New().AddExposure(Exposure{
				Name: "ok", Kind: ExposureKindView,
				ReadableBy: []string{reader},
			})
		})
	}
}

func TestAddExposure_AcceptsWildcardReaders(t *testing.T) {
	good := []string{
		"@me/analytics",
		"@*/analytics",
		"@me/oauth-*",
		"@*/oauth-*",
		"@me/*",
		"@*/*",
	}
	for _, reader := range good {
		r := New()
		r.AddExposure(Exposure{
			Name: "ok", Kind: ExposureKindView,
			ReadableBy: []string{reader},
		})
		// No panic = pass.
		_ = r.Exposures()
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
