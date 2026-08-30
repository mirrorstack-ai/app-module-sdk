package httpx

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseStrictQuery(t *testing.T) {
	t.Parallel()

	values, err := ParseStrictQuery("limit=25&status=active", "limit", "status", "cursor")
	if err != nil {
		t.Fatalf("ParseStrictQuery: %v", err)
	}
	if values.Get("limit") != "25" || values.Get("status") != "active" {
		t.Fatalf("values = %#v", values)
	}

	for _, raw := range []string{
		"unknown=value",
		"limit=10&limit=20",
		"limit=%zz",
		"limit=10;status=active",
	} {
		if _, err := ParseStrictQuery(raw, "limit", "status"); !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("ParseStrictQuery(%q) error = %v, want ErrInvalidQuery", raw, err)
		}
	}

	empty, err := ParseStrictQuery("", "limit")
	if err != nil || len(empty) != 0 {
		t.Errorf("empty query = %#v, %v", empty, err)
	}
}

func TestParseStrictLimit(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		raw  string
		want int
	}{
		{raw: "", want: 50},
		{raw: "1", want: 1},
		{raw: "200", want: 200},
	} {
		got, err := ParseStrictLimit(test.raw, 50, 200)
		if err != nil || got != test.want {
			t.Errorf("ParseStrictLimit(%q) = %d, %v, want %d", test.raw, got, err, test.want)
		}
	}
	for _, raw := range []string{"0", "201", "-1", "1.5", "ten", " 10"} {
		if _, err := ParseStrictLimit(raw, 50, 200); !errors.Is(err, ErrInvalidLimit) {
			t.Errorf("ParseStrictLimit(%q) error = %v, want ErrInvalidLimit", raw, err)
		}
	}
}

func TestParseStrictLimitPanicsForInvalidConfiguration(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatal("ParseStrictLimit did not panic")
		}
	}()
	_, _ = ParseStrictLimit("", 0, 200)
}

func TestScopedTimeIDCursor(t *testing.T) {
	t.Parallel()

	want := TimeIDCursor{
		Time: time.Date(2026, 8, 30, 3, 4, 5, 123456789, time.FixedZone("test", 8*60*60)),
		ID:   "123E4567-E89B-12D3-A456-426614174000",
	}
	scope := NewCursorScope("users", "active", "role=admin")
	raw := want.ScopedString(scope)
	if !strings.HasPrefix(raw, "cs1.") {
		t.Fatalf("ScopedString() = %q, want cs1 prefix", raw)
	}
	got, err := ParseScopedTimeIDCursor(raw, scope)
	if err != nil {
		t.Fatalf("ParseScopedTimeIDCursor: %v", err)
	}
	if !got.Time.Equal(want.Time) || got.ID != want.ID {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}

	if zero := (TimeIDCursor{}).ScopedString(CursorScope{}); zero != "" {
		t.Errorf("zero ScopedString = %q, want empty", zero)
	}
	if got, err := ParseScopedTimeIDCursor("", CursorScope{}); err != nil || !got.IsZero() {
		t.Errorf("empty cursor = %+v, %v, want zero, nil", got, err)
	}
}

func TestScopedTimeIDCursorRejectsPrefixlessEncoding(t *testing.T) {
	t.Parallel()

	scope := NewCursorScope("users", "active")
	want := TimeIDCursor{
		Time: time.Date(2026, 8, 30, 3, 4, 5, 0, time.UTC),
		ID:   "123e4567-e89b-12d3-a456-426614174000",
	}
	current := want.ScopedString(scope)
	prefixedPayload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(current, "cs1."))
	if err != nil {
		t.Fatal(err)
	}
	legacy := base64.RawURLEncoding.EncodeToString(prefixedPayload)
	if _, err := ParseScopedTimeIDCursor(legacy, scope); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("prefixless cursor error = %v, want ErrInvalidCursor", err)
	}
}

func TestScopedTimeIDCursorRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	scope := NewCursorScope("users", "active")
	otherScope := NewCursorScope("users", "disabled")
	valid := TimeIDCursor{
		Time: time.Date(2026, 8, 30, 3, 4, 5, 0, time.UTC),
		ID:   "123e4567-e89b-12d3-a456-426614174000",
	}.ScopedString(scope)

	badPayload := func(value map[string]any) string {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return "cs1." + base64.RawURLEncoding.EncodeToString(data)
	}

	encodedValid, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(valid, "cs1."))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encodedValid, &fields); err != nil {
		t.Fatal(err)
	}

	unknown := make(map[string]any, len(fields)+1)
	for key, value := range fields {
		unknown[key] = value
	}
	unknown["extra"] = true

	wrongID := make(map[string]any, len(fields))
	for key, value := range fields {
		wrongID[key] = value
	}
	wrongID["id"] = ""

	zeroTime := make(map[string]any, len(fields))
	for key, value := range fields {
		zeroTime[key] = value
	}
	zeroTime["time"] = time.Time{}

	for name, raw := range map[string]string{
		"garbage":         "%%%",
		"empty payload":   "cs1.",
		"unknown field":   badPayload(unknown),
		"empty id":        badPayload(wrongID),
		"zero time":       badPayload(zeroTime),
		"oversized":       strings.Repeat("x", 513),
		"unknown version": "cs2." + strings.TrimPrefix(valid, "cs1."),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseScopedTimeIDCursor(raw, scope); !errors.Is(err, ErrInvalidCursor) {
				t.Errorf("error = %v, want ErrInvalidCursor", err)
			}
		})
	}

	if _, err := ParseScopedTimeIDCursor(valid, otherScope); !errors.Is(err, ErrInvalidCursor) {
		t.Errorf("cross-scope error = %v, want ErrInvalidCursor", err)
	}
	if _, err := ParseScopedTimeIDCursor(valid, CursorScope{}); !errors.Is(err, ErrInvalidCursor) {
		t.Errorf("zero-scope error = %v, want ErrInvalidCursor", err)
	}
}

func TestScopedTimeIDCursorAcceptsGenericTieBreakID(t *testing.T) {
	t.Parallel()

	scope := NewCursorScope("ledger")
	want := TimeIDCursor{
		Time: time.Date(2026, 8, 30, 3, 4, 5, 0, time.UTC),
		ID:   "row-42",
	}
	got, err := ParseScopedTimeIDCursor(want.ScopedString(scope), scope)
	if err != nil || !got.Time.Equal(want.Time) || got.ID != want.ID {
		t.Fatalf("generic cursor = %+v, %v, want %+v", got, err, want)
	}
}

func TestNewCursorScopeSeparatesFilterTuples(t *testing.T) {
	t.Parallel()

	cursor := TimeIDCursor{
		Time: time.Date(2026, 8, 30, 3, 4, 5, 0, time.UTC),
		ID:   "123e4567-e89b-12d3-a456-426614174000",
	}
	left := cursor.ScopedString(NewCursorScope("users", "ab", "c"))
	right := cursor.ScopedString(NewCursorScope("users", "a", "bc"))
	if left == right {
		t.Fatal("length-distinct filter tuples produced the same scoped cursor")
	}
}
