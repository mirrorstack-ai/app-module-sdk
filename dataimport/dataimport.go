// Package dataimport is the module side of a bulk data import: a request flag
// carried in the context that a module's write paths honour, and the per-row
// result an import handler returns.
//
// The contract is source-agnostic. The SDK never knows where the rows came
// from; the importer owns the source, its keys and its formats. A module sees
// only an opaque run id, the source key of each row, and whether this call is
// a dry run.
//
// The platform sets the flag from the trusted invocation envelope, and only
// for a caller holding a short-lived import credential scoped to the app. A
// module never sets it from request input.
//
// In import mode:
//
//   - The module skips the side effects that belong to a live user action, not
//     to copying existing data. Ask Suppressed before each one: lifecycle
//     events, meter observations, transcode metering, the signup credit grant
//     and default role grants. meter.Client already drops every Record call.
//
//   - With DryRun, ms.Tx runs the same code and then rolls back instead of
//     committing, so the result is computed exactly as an apply would compute
//     it and nothing is written. A write made outside ms.Tx is NOT rolled back:
//     an import write path must use ms.Tx.
//
//   - Each row returns a RowResult: inserted, updated, unchanged or
//     rejected(reason), with its provenance.
//
// An import handler:
//
//	func importUsers(w http.ResponseWriter, r *http.Request) {
//	    ctx := r.Context()
//	    if !dataimport.Active(ctx) {
//	        http.Error(w, "import mode required", http.StatusForbidden)
//	        return
//	    }
//	    var results []dataimport.RowResult
//	    for _, in := range rows {
//	        row := dataimport.NewRow(ctx, in.SourceKey, in.SourceHash)
//	        err := ms.Tx(ctx, func(q db.Querier) error {
//	            res, err := upsertUser(ctx, q, in, row)
//	            results = append(results, res)
//	            return err
//	        })
//	        ...
//	    }
//	    ms.WriteJSON(w, http.StatusOK, dataimport.NewReport(ctx, results))
//	}
package dataimport

import (
	"context"
	"errors"
	"regexp"
)

type contextKey string

const modeKey = contextKey("ms-data-import")

// Mode is the import flag for one request.
type Mode struct {
	// RunID is the importer's opaque id for this run. It is written into
	// every row's provenance so a run can be audited and rolled back.
	RunID string `json:"runId"`
	// DryRun computes the full result and writes nothing: ms.Tx rolls back.
	DryRun bool `json:"dryRun,omitempty"`
}

// runIDPattern bounds RunID to an opaque token: it lands in provenance
// columns and logs, so it must never carry free text.
var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// ErrInvalidRunID is returned by Validate for an empty or malformed RunID.
var ErrInvalidRunID = errors.New("mirrorstack/dataimport: run id must be 1-128 chars of [A-Za-z0-9._:-]")

// Validate reports whether m can be carried on a request.
func (m Mode) Validate() error {
	if !runIDPattern.MatchString(m.RunID) {
		return ErrInvalidRunID
	}
	return nil
}

// With returns a context in import mode. Set by the Lambda invoke shim
// (runtime.InjectResources) from the trusted envelope, and by tests. Never
// set it from module input.
func With(ctx context.Context, m Mode) context.Context {
	return context.WithValue(ctx, modeKey, m)
}

// From returns the import mode of ctx, and false outside import mode.
func From(ctx context.Context) (Mode, bool) {
	m, ok := ctx.Value(modeKey).(Mode)
	return m, ok
}

// Active reports whether ctx is in import mode (dry run or apply).
func Active(ctx context.Context) bool {
	_, ok := From(ctx)
	return ok
}

// DryRun reports whether ctx is an import dry run. ms.Tx rolls back when it
// is true.
func DryRun(ctx context.Context) bool {
	m, ok := From(ctx)
	return ok && m.DryRun
}

// Effect names a side effect a module write path skips in import mode. The
// names are the SDK's vocabulary, so every module asks about the same thing
// in the same words and a reviewer can grep for each one.
type Effect string

const (
	// LifecycleOutbox is a row in the module's lifecycle_outbox (user.created,
	// video.ready, …). Imported rows existed before; announcing them would
	// fire every consumer's "new" reaction.
	LifecycleOutbox Effect = "lifecycle_outbox"
	// MeterOutbox is a row in the module's meter_outbox. Copying data is not
	// usage and must not bill.
	MeterOutbox Effect = "meter_outbox"
	// TranscodeMeterOutbox is a row in video_transcode_meter_outbox.
	TranscodeMeterOutbox Effect = "transcode_meter_outbox"
	// SignupCreditGrant is the credit granted to a newly signed-up user.
	SignupCreditGrant Effect = "signup_credit_grant"
	// DefaultRoleGrant is the role granted to a new user by default. An
	// imported user gets exactly the roles the import maps, never a default.
	DefaultRoleGrant Effect = "default_role_grant"
)

// Suppressed reports whether the write path must skip effect e. Every Effect
// is suppressed in import mode, dry run or apply.
//
//	if !dataimport.Suppressed(ctx, dataimport.LifecycleOutbox) {
//	    if err := q.QueueLifecycleEvent(ctx, ev); err != nil { return err }
//	}
func Suppressed(ctx context.Context, e Effect) bool {
	return Active(ctx)
}
