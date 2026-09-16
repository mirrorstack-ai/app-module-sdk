package core

import (
	"errors"
	"reflect"
	"testing"
)

// TestBuildDynamicSelect_GoldenMatchesApiPlatform is the cross-repo conformance
// GOLDEN: the ported buildDynamicSelect must emit byte-identical (SQL text +
// args) output to api-platform's internal/shared/database.QueryDynamicSelect
// (buildDynamicSelect) for a representative shape. The SDK cannot import
// api-platform, so this frozen string IS the contract — if either side's
// composition drifts (projection order, quoting, $n numbering, limit+1,
// WHERE/AND joins), this test fails closed rather than letting the deployed
// read silently diverge from the blessed platform builder.
//
// Shape mirrors the real oauth-google → oauth-core users read: a physical
// relation the platform computed via ids.PhysicalTableName
// (m<uuid-hex>_users), a projection, one equality filter, one IN filter, and a
// caller limit (asks for limit+1 to detect truncation).
func TestBuildDynamicSelect_GoldenMatchesApiPlatform(t *testing.T) {
	q := DynamicSelect{
		Schema:  "app_283e0ef9_1a2b_3c4d_5e6f_0123456789ab",
		Table:   "m81b3ac7081c1409495700c761e23b59e_users",
		Columns: []string{"id", "display_name", "email"},
		Filters: []SelectFilter{
			{Column: "status", Values: []any{"active"}},
			{Column: "id", Values: []any{1, 2, 3}},
		},
		Limit: 500,
	}

	const wantSQL = `SELECT "id", "display_name", "email" ` +
		`FROM "app_283e0ef9_1a2b_3c4d_5e6f_0123456789ab"."m81b3ac7081c1409495700c761e23b59e_users" ` +
		`WHERE "status" = $1 AND "id" IN ($2, $3, $4) LIMIT 501`
	wantArgs := []any{"active", 1, 2, 3}

	gotSQL, gotArgs, limit, err := buildDynamicSelect(q)
	if err != nil {
		t.Fatalf("buildDynamicSelect: %v", err)
	}
	if gotSQL != wantSQL {
		t.Errorf("SQL drift from api-platform QueryDynamicSelect:\n got: %s\nwant: %s", gotSQL, wantSQL)
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Errorf("args = %#v, want %#v", gotArgs, wantArgs)
	}
	if limit != 500 {
		t.Errorf("effective limit = %d, want 500 (SQL asks for limit+1 = 501)", limit)
	}
}

func TestBuildDynamicSelect_LimitClampAndDefault(t *testing.T) {
	cases := []struct {
		name      string
		limit     int
		wantLimit int // effective; SQL asks for wantLimit+1
	}{
		{"zero → default", 0, defaultDynamicSelectLimit},
		{"negative → default", -5, defaultDynamicSelectLimit},
		{"in range kept", 750, 750},
		{"above ceiling → clamped", 9999, maxDynamicSelectLimit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, limit, err := buildDynamicSelect(DynamicSelect{
				Schema: "app_x", Table: "m_users", Columns: []string{"id"}, Limit: tc.limit,
			})
			if err != nil {
				t.Fatalf("buildDynamicSelect: %v", err)
			}
			if limit != tc.wantLimit {
				t.Errorf("effective limit = %d, want %d", limit, tc.wantLimit)
			}
		})
	}
}

func TestBuildDynamicSelect_ShapeGateRejectsUnsafeIdentifiers(t *testing.T) {
	base := DynamicSelect{Schema: "app_x", Table: "m_users", Columns: []string{"id"}}

	mutate := map[string]func(DynamicSelect) DynamicSelect{
		"schema with quote":    func(q DynamicSelect) DynamicSelect { q.Schema = `app_x"; drop`; return q },
		"schema empty":         func(q DynamicSelect) DynamicSelect { q.Schema = ""; return q },
		"table with semicolon": func(q DynamicSelect) DynamicSelect { q.Table = "m_users; drop table x"; return q },
		"table with dot":       func(q DynamicSelect) DynamicSelect { q.Table = "public.users"; return q },
		"uppercase column":     func(q DynamicSelect) DynamicSelect { q.Columns = []string{"Id"}; return q },
		"column with dash":     func(q DynamicSelect) DynamicSelect { q.Columns = []string{"e-mail"}; return q },
		"filter col injection": func(q DynamicSelect) DynamicSelect {
			q.Filters = []SelectFilter{{Column: "id = 1 OR", Values: []any{1}}}
			return q
		},
	}
	for name, m := range mutate {
		t.Run(name, func(t *testing.T) {
			_, _, _, err := buildDynamicSelect(m(base))
			if !errors.Is(err, errUnsafeSelectIdentifier) {
				t.Errorf("err = %v, want errUnsafeSelectIdentifier (fail closed before any SQL text)", err)
			}
		})
	}
}

func TestBuildDynamicSelect_RequiresColumnsAndFilterValues(t *testing.T) {
	if _, _, _, err := buildDynamicSelect(DynamicSelect{Schema: "app_x", Table: "m_users"}); err == nil {
		t.Errorf("no columns: err = nil, want error")
	}
	// A filter with an empty Values list is a bug (an always-true/false shape);
	// it must error, never emit a dangling WHERE.
	_, _, _, err := buildDynamicSelect(DynamicSelect{
		Schema: "app_x", Table: "m_users", Columns: []string{"id"},
		Filters: []SelectFilter{{Column: "id", Values: nil}},
	})
	if err == nil {
		t.Errorf("empty filter values: err = nil, want error")
	}
}

// TestNormalizeUUIDValues_DecodedShape asserts on the shape pgx ACTUALLY
// returns for a uuid column, not on a shape a fixture invented.
//
// The control is the second row: a uuid that already arrived as text (the
// JSON/dev-proxy plane) must pass through untouched, so the two planes agree
// rather than one of them being rewritten twice.
func TestNormalizeUUIDValues_DecodedShape(t *testing.T) {
	raw := [16]byte{0x3b, 0xc6, 0xdb, 0xe1, 0xc5, 0xb8, 0x4d, 0x77, 0x9d, 0xa5, 0x2d, 0xeb, 0x1c, 0xcc, 0xcf, 0x4e}
	const want = "3bc6dbe1-c5b8-4d77-9da5-2deb1ccccf4e"

	rows := []map[string]any{
		{"id": raw, "title": "a video", "duration_sec": 154.2, "deleted_at": nil},
		{"id": "already-text", "tags": [][16]byte{raw}},
	}
	normalizeUUIDValues(rows)

	if got, ok := rows[0]["id"].(string); !ok || got != want {
		t.Errorf("id = %#v (%T), want the canonical text %q", rows[0]["id"], rows[0]["id"], want)
	}
	if rows[0]["title"] != "a video" || rows[0]["duration_sec"] != 154.2 || rows[0]["deleted_at"] != nil {
		t.Errorf("non-uuid columns were rewritten: %#v", rows[0])
	}
	if rows[1]["id"] != "already-text" {
		t.Errorf("a value that is already text must pass through, got %#v", rows[1]["id"])
	}
	tags, ok := rows[1]["tags"].([]any)
	if !ok || len(tags) != 1 || tags[0] != want {
		t.Errorf("uuid[] = %#v, want []any{%q}", rows[1]["tags"], want)
	}
}

// TestUUIDText_CanonicalForm locks the rendering: lowercase hex in the
// 8-4-4-4-12 grouping Postgres prints, including the leading zeros a %x drops.
func TestUUIDText_CanonicalForm(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   [16]byte
		want string
	}{
		{"zero", [16]byte{}, "00000000-0000-0000-0000-000000000000"},
		{"leading zero octets", [16]byte{0x00, 0x0f, 0x00, 0x01}, "000f0001-0000-0000-0000-000000000000"},
		{"all set", [16]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, "ffffffff-ffff-ffff-ffff-ffffffffffff"},
	} {
		if got := uuidText(tc.in); got != tc.want {
			t.Errorf("%s: uuidText = %q, want %q", tc.name, got, tc.want)
		}
	}
}
