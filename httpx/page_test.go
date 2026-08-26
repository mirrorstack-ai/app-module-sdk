package httpx

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLimit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{"", 50},
		{"10", 10},
		{"0", 1},
		{"1", 1},
		{"5000", 200},
		{"not-a-number", 50}, // a page size is a hint; do not fail a read over it
		{"-3", 1},
	} {
		r := httptest.NewRequest("GET", "/x?limit="+tc.raw, nil)
		if got := Limit(r, 50, 200); got != tc.want {
			t.Errorf("Limit(limit=%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}

// A cursor must survive the round trip exactly, or a page boundary lands in the
// wrong place and rows are shown twice or skipped.
func TestCursorRoundTrip(t *testing.T) {
	t.Parallel()

	t.Run("id", func(t *testing.T) {
		t.Parallel()
		for _, want := range []IDCursor{1, 42, 1 << 40} {
			r := httptest.NewRequest("GET", "/x?cursor="+want.String(), nil)
			if got := ParseIDCursor(r); got != want {
				t.Errorf("round trip of %d = %d", want, got)
			}
		}
		// The zero cursor encodes as "", so a caller can hand it straight to
		// NextCursor with no special case.
		if s := IDCursor(0).String(); s != "" {
			t.Errorf("IDCursor(0).String() = %q, want empty", s)
		}
	})

	t.Run("time+id keeps nanoseconds", func(t *testing.T) {
		t.Parallel()
		// 🔴 The nanoseconds are the point. A second-resolution cursor on a
		// table that writes faster than that loses rows at every boundary.
		want := TimeIDCursor{
			Time: time.Date(2026, 8, 27, 4, 5, 6, 123456789, time.UTC),
			ID:   "9f1c2d3e-4a5b-6c7d-8e9f-0a1b2c3d4e5f",
		}
		r := httptest.NewRequest("GET", "/x?cursor="+want.String(), nil)
		got := ParseTimeIDCursor(r)
		if !got.Time.Equal(want.Time) || got.ID != want.ID {
			t.Errorf("round trip = %+v, want %+v", got, want)
		}
	})

	t.Run("an id containing the separator cannot split the cursor", func(t *testing.T) {
		t.Parallel()
		// A unit separator was chosen over '|' precisely because a value can
		// contain '|'. This pins that reasoning.
		want := TimeIDCursor{Time: time.Unix(0, 0).UTC(), ID: "a|b|c"}
		r := httptest.NewRequest("GET", "/x?cursor="+want.String(), nil)
		if got := ParseTimeIDCursor(r); got.ID != want.ID {
			t.Errorf("id = %q, want %q", got.ID, want.ID)
		}
	})
}

// An undecodable cursor must read as the FIRST page, not as an error: it is
// the same answer as no cursor, it cannot leak whether some other row exists,
// and a stale cursor from an old client degrades to "start over" rather than a
// 400 nobody can act on.
func TestGarbageCursorIsTheFirstPage(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"garbage", "c1.%%%", "c1.", "-1", "c1." + IDCursor(-5).String()} {
		r := httptest.NewRequest("GET", "/x?cursor="+raw, nil)
		if got := ParseIDCursor(r); got != 0 {
			t.Errorf("ParseIDCursor(%q) = %d, want 0", raw, got)
		}
		if got := ParseTimeIDCursor(r); !got.IsZero() {
			t.Errorf("ParseTimeIDCursor(%q) = %+v, want zero", raw, got)
		}
	}
}

func TestNewPage(t *testing.T) {
	t.Parallel()
	type row struct{ ID int64 }
	cursorOf := func(r row) string { return IDCursor(r.ID).String() }

	t.Run("last page has a null cursor", func(t *testing.T) {
		t.Parallel()
		p := NewPage([]row{{1}, {2}}, false, cursorOf)
		if p.NextCursor != nil {
			t.Errorf("NextCursor = %v, want nil — clients test for null", *p.NextCursor)
		}
	})

	t.Run("more pages carry the last row's cursor", func(t *testing.T) {
		t.Parallel()
		p := NewPage([]row{{1}, {7}}, true, cursorOf)
		if p.NextCursor == nil || *p.NextCursor != IDCursor(7).String() {
			t.Errorf("NextCursor = %v, want the LAST row's cursor", p.NextCursor)
		}
	})

	t.Run("an empty page marshals as [] and never null", func(t *testing.T) {
		t.Parallel()
		body, err := json.Marshal(NewPage(nil, false, cursorOf))
		if err != nil {
			t.Fatal(err)
		}
		// A null items array is a special case every client would have to
		// handle, forever, for no reason.
		if got := string(body); got != `{"items":[]}` {
			t.Errorf("empty page = %s, want {\"items\":[]}", got)
		}
	})

	t.Run("truncated is not the same as more-pages", func(t *testing.T) {
		t.Parallel()
		p := NewPage([]row{{1}}, true, cursorOf).Truncate("one row exceeded the size cap")
		if !p.Truncated || p.Note == "" {
			t.Errorf("Truncate did not mark the page: %+v", p)
		}
		if p.NextCursor == nil {
			t.Error("Truncate cleared NextCursor — a page can be both cut AND have successors")
		}
	})
}
