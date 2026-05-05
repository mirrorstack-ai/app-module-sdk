package registry

import (
	"slices"
	"strings"
	"testing"
)

func TestAddExposure_StoresAndReturns(t *testing.T) {
	r := New()
	r.AddExposure(Exposure{
		Name:       "recent_orders",
		Kind:       ExposureKindView,
		ReadableBy: []string{"@anna/analytics"},
	})
	out := r.Exposures()
	if len(out) != 1 || out[0].Name != "recent_orders" || out[0].Kind != ExposureKindView {
		t.Fatalf("unexpected exposures: %+v", out)
	}
	if !slices.Equal(out[0].ReadableBy, []string{"@anna/analytics"}) {
		t.Errorf("ReadableBy round-trip lost: %+v", out[0].ReadableBy)
	}
}

func TestAddExposure_MergesReadersAcrossCalls(t *testing.T) {
	r := New()
	r.AddExposure(Exposure{Name: "links", Kind: ExposureKindView, ReadableBy: []string{"@anna/oauth-google"}})
	r.AddExposure(Exposure{Name: "links", Kind: ExposureKindView, ReadableBy: []string{"@anna/oauth-github"}})
	out := r.Exposures()
	if len(out) != 1 {
		t.Fatalf("expected dedup to keep one entry, got %d", len(out))
	}
	if !slices.Equal(out[0].ReadableBy, []string{"@anna/oauth-google", "@anna/oauth-github"}) {
		t.Errorf("expected merge-union, got %+v", out[0].ReadableBy)
	}
}

func TestAddExposure_MergeDeduplicatesIdenticalReader(t *testing.T) {
	r := New()
	r.AddExposure(Exposure{Name: "x", Kind: ExposureKindView, ReadableBy: []string{"@anna/a", "@anna/b"}})
	r.AddExposure(Exposure{Name: "x", Kind: ExposureKindView, ReadableBy: []string{"@anna/b", "@anna/c"}})
	out := r.Exposures()
	if !slices.Equal(out[0].ReadableBy, []string{"@anna/a", "@anna/b", "@anna/c"}) {
		t.Errorf("expected dedup-on-merge, got %+v", out[0].ReadableBy)
	}
}

func TestAddExposure_PanicsOnKindConflict(t *testing.T) {
	assertExposurePanics(t, "view→table on same name should panic", func() {
		r := New()
		r.AddExposure(Exposure{Name: "x", Kind: ExposureKindView})
		r.AddExposure(Exposure{Name: "x", Kind: ExposureKindTable})
	})
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
		ReadableBy: []string{"@anna/analytics"},
	})
	clone := r.Exposures()
	clone[0].ReadableBy[0] = "@anna/mutated"
	again := r.Exposures()
	if again[0].ReadableBy[0] != "@anna/analytics" {
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

func TestAddExposure_NameLengthBoundary(t *testing.T) {
	// 63 chars: max accepted (Postgres NAMEDATALEN ceiling).
	max := "a" + strings.Repeat("b", 62) // 1 + 62 = 63
	r := New()
	r.AddExposure(Exposure{Name: max, Kind: ExposureKindView})
	if r.Exposures()[0].Name != max {
		t.Error("63-char name should accept")
	}

	// 64 chars: one over.
	overflow := "a" + strings.Repeat("b", 63) // 1 + 63 = 64
	assertExposurePanics(t, "64-char name should panic", func() {
		New().AddExposure(Exposure{Name: overflow, Kind: ExposureKindView})
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
		// Wildcards are not supported. Matching "any owner's analytics
		// module" only makes sense if the platform has a module-spec
		// system declaring what a module named `analytics` must implement;
		// we don't, so each consumer is listed explicitly.
		"@*/analytics",
		"@me/oauth-*",
		"@*/oauth-*",
		"@me/*",
		"@*/*",
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

func TestAddExposure_AcceptsExactReaders(t *testing.T) {
	good := []string{
		"@anna/analytics",
		"@anna/oauth-google",
		"@bob/dashboard",
		"@security/audit-collector",
		// Single-char halves at the lower bound.
		"@a/b",
		// Underscores allowed; mirrors Config.ID's regex.
		"@anna/billing_engine",
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
