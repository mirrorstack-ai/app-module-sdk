package dataimport

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestModeOutsideImport(t *testing.T) {
	ctx := context.Background()
	if Active(ctx) || DryRun(ctx) {
		t.Fatal("a plain context must not be in import mode")
	}
	for _, e := range []Effect{LifecycleOutbox, MeterOutbox, TranscodeMeterOutbox, SignupCreditGrant, DefaultRoleGrant} {
		if Suppressed(ctx, e) {
			t.Errorf("%s suppressed outside import mode", e)
		}
	}
}

func TestModeApplyAndDryRun(t *testing.T) {
	for _, dry := range []bool{false, true} {
		ctx := With(context.Background(), Mode{RunID: "run-1", DryRun: dry})
		if !Active(ctx) {
			t.Fatalf("dry=%v: Active = false", dry)
		}
		if DryRun(ctx) != dry {
			t.Fatalf("DryRun = %v, want %v", DryRun(ctx), dry)
		}
		for _, e := range []Effect{LifecycleOutbox, MeterOutbox, TranscodeMeterOutbox, SignupCreditGrant, DefaultRoleGrant} {
			if !Suppressed(ctx, e) {
				t.Errorf("dry=%v: %s not suppressed in import mode", dry, e)
			}
		}
		m, ok := From(ctx)
		if !ok || m.RunID != "run-1" {
			t.Fatalf("From = %+v, %v", m, ok)
		}
	}
}

func TestModeValidate(t *testing.T) {
	for _, id := range []string{"run-1", "2026-09-23T10:00:00Z", "a", strings.Repeat("x", 128)} {
		if err := (Mode{RunID: id}).Validate(); err != nil {
			t.Errorf("RunID %q rejected: %v", id, err)
		}
	}
	for _, id := range []string{"", " run", "-run", "run 1", "run/1", "rün", strings.Repeat("x", 129)} {
		if err := (Mode{RunID: id}).Validate(); err == nil {
			t.Errorf("RunID %q accepted", id)
		}
	}
}

func TestRowOutcomesCarryProvenance(t *testing.T) {
	ctx := With(context.Background(), Mode{RunID: "run-7", DryRun: true})
	row := NewRow(ctx, "src-42", "h1")
	want := Provenance{RunID: "run-7", SourceKey: "src-42", SourceHash: "h1", DryRun: true}

	cases := []struct {
		got     RowResult
		outcome Outcome
		target  string
	}{
		{row.Inserted("t-1"), Inserted, "t-1"},
		{row.Updated("t-2", map[string]any{"name": "old"}), Updated, "t-2"},
		{row.Unchanged("t-3"), Unchanged, "t-3"},
		{row.Rejected("unmapped_role"), Rejected, ""},
	}
	for _, c := range cases {
		if c.got.Outcome != c.outcome || c.got.TargetID != c.target || c.got.Provenance != want {
			t.Errorf("%s: got %+v", c.outcome, c.got)
		}
	}
	if cases[1].got.PriorValues["name"] != "old" {
		t.Errorf("Updated lost prior values: %+v", cases[1].got)
	}
	if cases[3].got.Reason != "unmapped_role" {
		t.Errorf("Rejected reason = %q", cases[3].got.Reason)
	}
}

func TestRejectedReasonIsACodeNeverAValue(t *testing.T) {
	row := NewRow(With(context.Background(), Mode{RunID: "r"}), "k", "")
	for _, reason := range []string{"", "user alice@example.com has no role", "Unmapped", "9lives", strings.Repeat("a", 65)} {
		if got := row.Rejected(reason).Reason; got != InvalidReason {
			t.Errorf("Rejected(%q).Reason = %q, want %q", reason, got, InvalidReason)
		}
	}
	if got := row.Rejected("price_not_positive").Reason; got != "price_not_positive" {
		t.Errorf("valid code rewritten to %q", got)
	}
}

func TestNewReportCountsAndWire(t *testing.T) {
	ctx := With(context.Background(), Mode{RunID: "run-9", DryRun: true})
	row := NewRow(ctx, "k", "")
	rep := NewReport(ctx, []RowResult{row.Inserted("a"), row.Inserted("b"), row.Updated("c", nil), row.Unchanged("d"), row.Rejected("dup")})
	if rep.Counts != (Counts{Inserted: 2, Updated: 1, Unchanged: 1, Rejected: 1}) {
		t.Fatalf("Counts = %+v", rep.Counts)
	}
	if rep.RunID != "run-9" || !rep.DryRun {
		t.Fatalf("report header = %+v", rep)
	}

	b, err := json.Marshal(rep.Rows[4])
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"outcome":"rejected","reason":"dup","provenance":{"runId":"run-9","sourceKey":"k","dryRun":true}}`
	if string(b) != want {
		t.Errorf("wire = %s\nwant   %s", b, want)
	}

	empty, _ := json.Marshal(NewReport(ctx, nil))
	if !strings.Contains(string(empty), `"rows":[]`) {
		t.Errorf("empty report must carry rows:[], got %s", empty)
	}
}
