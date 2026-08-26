package httpx

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
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

// Cap must drop whole ITEMS, never bytes.
//
// 🔴 THE OBVIOUS IMPLEMENTATION IS THE BROKEN ONE. Marshalling and cutting the
// string is how most size caps get written, and it produces INVALID JSON — the
// reader gets a parse error instead of data, or a half-written object it
// silently mis-reads. Every case below asserts the result is still parseable.
func TestCap(t *testing.T) {
	t.Parallel()
	type row struct {
		ID   int64  `json:"id"`
		Body string `json:"body"`
	}
	cursorOf := func(r row) string { return IDCursor(r.ID).String() }

	items := make([]row, 40)
	for i := range items {
		items[i] = row{ID: int64(i + 1), Body: strings.Repeat("x", 100)}
	}

	t.Run("everything fits", func(t *testing.T) {
		t.Parallel()
		p := Cap(items, 1<<20, cursorOf)
		if len(p.Items) != len(items) || p.Truncated {
			t.Errorf("Cap dropped %d items that fit", len(items)-len(p.Items))
		}
		if p.NextCursor != nil {
			t.Error("a complete page carries a NextCursor")
		}
	})

	t.Run("stays valid JSON and says it was cut", func(t *testing.T) {
		t.Parallel()
		p := Cap(items, 900, cursorOf)
		if !p.Truncated || p.Note == "" {
			t.Fatalf("Cap dropped items without saying so: %+v", p)
		}
		if len(p.Items) == 0 || len(p.Items) == len(items) {
			t.Fatalf("kept %d of %d items", len(p.Items), len(items))
		}
		body, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("a truncated page must still marshal: %v", err)
		}
		var round Page[row]
		if err := json.Unmarshal(body, &round); err != nil {
			t.Fatalf("a truncated page must still PARSE — this is the whole point: %v", err)
		}
		if len(round.Items) != len(p.Items) {
			t.Errorf("round trip changed the item count: %d vs %d", len(round.Items), len(p.Items))
		}
		// And it must say where to resume, or the caller cannot recover the rest.
		if round.NextCursor == nil {
			t.Error("a truncated page carries no cursor, so the dropped items are unreachable")
		}
		if !strings.Contains(round.Note, "omitted") {
			t.Errorf("Note = %q, want it to say what happened", round.Note)
		}
	})

	t.Run("a single oversized item still yields a valid page", func(t *testing.T) {
		t.Parallel()
		huge := []row{{ID: 1, Body: strings.Repeat("x", 5000)}}
		p := Cap(huge, 100, cursorOf)
		body, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := json.Unmarshal(body, &Page[row]{}); err != nil {
			t.Fatalf("must still parse: %v", err)
		}
		// Nothing fit, so the page is empty AND says so — never a silent
		// empty list the caller reads as "there is nothing".
		if len(p.Items) != 0 || !p.Truncated {
			t.Errorf("page = %+v, want empty and marked truncated", p)
		}
	})

	t.Run("zero maxBytes falls back to the default", func(t *testing.T) {
		t.Parallel()
		if p := Cap(items, 0, cursorOf); len(p.Items) != len(items) {
			t.Errorf("kept %d of %d under the default cap", len(p.Items), len(items))
		}
	})
}
