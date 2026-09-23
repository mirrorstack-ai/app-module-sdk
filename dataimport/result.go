package dataimport

import (
	"context"
	"regexp"
)

// Outcome is what an import did to one row.
type Outcome string

const (
	// Inserted: the row did not exist and was created.
	Inserted Outcome = "inserted"
	// Updated: the row existed and fields the import owns changed.
	Updated Outcome = "updated"
	// Unchanged: the row existed and nothing the import owns differed.
	Unchanged Outcome = "unchanged"
	// Rejected: the row was not written; Reason says why.
	Rejected Outcome = "rejected"
)

// Provenance ties a row result to the run and the source row that produced
// it. The importer stores it in its map so a later run can skip an unchanged
// row, find a deleted one, and roll a run back.
type Provenance struct {
	RunID      string `json:"runId"`
	SourceKey  string `json:"sourceKey"`
	SourceHash string `json:"sourceHash,omitempty"`
	DryRun     bool   `json:"dryRun,omitempty"`
}

// RowResult is the result of importing one row.
type RowResult struct {
	Outcome Outcome `json:"outcome"`
	// TargetID is the module's id for the row: the new id for Inserted, the
	// existing id otherwise. Empty for Rejected.
	TargetID string `json:"targetId,omitempty"`
	// Reason is a machine code for Rejected, such as "unmapped_role". It must
	// never carry a row's values: reports are shared and logged.
	Reason string `json:"reason,omitempty"`
	// PriorValues holds, for Updated, the values of only the fields the import
	// overwrote, so a rollback can restore them.
	PriorValues map[string]any `json:"priorValues,omitempty"`
	Provenance  Provenance     `json:"provenance"`
}

// Row builds the result for one source row. Take it with NewRow before the
// write, then return one of its outcomes.
type Row struct {
	provenance Provenance
}

// NewRow starts the result for the source row sourceKey. sourceHash is the
// importer's hash of the source row, echoed back into provenance; pass "" if
// the importer sent none.
func NewRow(ctx context.Context, sourceKey, sourceHash string) Row {
	m, _ := From(ctx)
	return Row{provenance: Provenance{
		RunID:      m.RunID,
		SourceKey:  sourceKey,
		SourceHash: sourceHash,
		DryRun:     m.DryRun,
	}}
}

// Inserted reports that the row was created with id targetID.
func (r Row) Inserted(targetID string) RowResult {
	return RowResult{Outcome: Inserted, TargetID: targetID, Provenance: r.provenance}
}

// Updated reports that the existing row targetID changed. prior holds the
// overwritten fields' previous values.
func (r Row) Updated(targetID string, prior map[string]any) RowResult {
	return RowResult{Outcome: Updated, TargetID: targetID, PriorValues: prior, Provenance: r.provenance}
}

// Unchanged reports that the existing row targetID already matched.
func (r Row) Unchanged(targetID string) RowResult {
	return RowResult{Outcome: Unchanged, TargetID: targetID, Provenance: r.provenance}
}

// reasonPattern keeps Reason a code, never a sentence that could quote a
// value from the row.
var reasonPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// InvalidReason is the Reason a Rejected result carries when the module's own
// reason was not a valid code, so a bad reason never leaks into a report.
const InvalidReason = "invalid_reason"

// Rejected reports that the row was not written. reason is a snake_case code
// of at most 64 chars, such as "unmapped_role" or "price_not_positive"; any
// other value is replaced by InvalidReason.
func (r Row) Rejected(reason string) RowResult {
	if !reasonPattern.MatchString(reason) {
		reason = InvalidReason
	}
	return RowResult{Outcome: Rejected, Reason: reason, Provenance: r.provenance}
}

// Counts totals a batch by outcome.
type Counts struct {
	Inserted  int `json:"inserted"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
	Rejected  int `json:"rejected"`
}

// Report is the response body of an import handler.
type Report struct {
	RunID  string      `json:"runId"`
	DryRun bool        `json:"dryRun"`
	Counts Counts      `json:"counts"`
	Rows   []RowResult `json:"rows"`
}

// NewReport totals rows into the response for the run in ctx.
func NewReport(ctx context.Context, rows []RowResult) Report {
	m, _ := From(ctx)
	rep := Report{RunID: m.RunID, DryRun: m.DryRun, Rows: rows}
	if rep.Rows == nil {
		rep.Rows = []RowResult{}
	}
	for _, row := range rows {
		switch row.Outcome {
		case Inserted:
			rep.Counts.Inserted++
		case Updated:
			rep.Counts.Updated++
		case Unchanged:
			rep.Counts.Unchanged++
		case Rejected:
			rep.Counts.Rejected++
		}
	}
	return rep
}
