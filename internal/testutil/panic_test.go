package testutil

import (
	"fmt"
	"strings"
	"testing"
)

// recordingTB captures Errorf so the failure path of AssertPanics is testable
// without failing the real test.
type recordingTB struct {
	testing.TB
	msgs []string
}

func (r *recordingTB) Helper() {}
func (r *recordingTB) Errorf(format string, args ...any) {
	r.msgs = append(r.msgs, strings.TrimSpace(fmt.Sprintf(format, args...)))
}

func TestAssertPanics_PassesWhenFnPanics(t *testing.T) {
	rec := &recordingTB{TB: t}
	AssertPanics(rec, func() { panic("boom") }, "should not be reported")
	if len(rec.msgs) != 0 {
		t.Fatalf("unexpected failure: %v", rec.msgs)
	}
}

func TestAssertPanics_FailsWhenFnReturns(t *testing.T) {
	rec := &recordingTB{TB: t}
	AssertPanics(rec, func() {}, "expected panic for %q", "x")
	if len(rec.msgs) != 1 || rec.msgs[0] != `expected panic for "x"` {
		t.Fatalf("msgs = %v, want one formatted failure", rec.msgs)
	}
}
