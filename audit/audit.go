// Package audit records what a module did, into the platform's audit store.
//
// # Why the platform owns this
//
// Nine modules grew nine differently-shaped audit tables with six different
// names for the actor column, and the cross-module READ contract ossified
// around one module's vocabulary. credit renders a 50-credit debit as
// action:"revoked", roleKey:"deduct" because that is the only shape the host
// knows how to read. One store with a domain-neutral shape dissolves that.
//
// # Why Entry has no actor
//
// 🔴 This is the design, not an omission. Every per-module table this replaces
// took the actor from whatever the caller supplied — one module writes a URL
// path parameter as the actor on an identity-free internal surface, so its
// trail records whoever the CALLER SAID it was, which is not an audit trail.
//
// The platform stamps the actor from the authenticated request identity. There
// is no parameter for it here, so a caller-supplied actor is not discouraged —
// it is unexpressible.
//
// # Why it writes to a local outbox first
//
// Record takes the module's own transaction and writes into a table in the
// module's own schema. That is what makes the audit row atomic with the change
// it describes: a mutation that commits always has its audit row, and one that
// rolls back leaves none.
//
// The alternative — calling the platform inline — puts a network round trip
// inside a database transaction and forces a choice between losing the audit
// row and failing the mutation. Neither is acceptable for a record whose whole
// value is that it is complete.
//
// A drain moves outbox rows to the platform afterwards. Delivery is therefore
// AT-LEAST-ONCE and can lag; the platform de-duplicates on the outbox id.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/pgerr"
)

// Entry is one thing that happened.
//
// SubjectKind and Action are YOUR words — "certificate", "debited". The
// platform never interprets either, which is what keeps one module's vocabulary
// from becoming everyone's.
type Entry struct {
	// SubjectKind is what sort of thing this happened to ("user",
	// "certificate", "credit-account").
	SubjectKind string
	// SubjectID identifies it within that kind.
	SubjectID string
	// Action is what happened, in the past tense ("granted", "debited",
	// "revoked").
	Action string
	// Details is optional structured context. Keep it small: audit rows are
	// read back into an agent's context and rendered in a host's UI, so an
	// oversized blob is your module deciding how much of someone else's screen
	// it gets. The platform caps it.
	Details any
}

var ErrInvalidEntry = errors.New("mirrorstack/audit: an entry needs a subject kind, a subject id and an action")

// maxDetailBytes mirrors the platform's cap so an entry that the platform will
// refuse fails HERE — inside the caller's transaction, where the module can
// still do something about it — rather than later, in a drain, where the only
// options are drop it or retry it forever.
const maxDetailBytes = 8 << 10

// Record appends one entry to the module's local outbox, inside the caller's
// transaction.
//
// Pass the SAME querier the mutation is using. That is the entire point: the
// audit row and the change it describes commit together or not at all.
//
//	return ms.Tx(ctx, func(q db.Querier) error {
//	    if err := debit(ctx, q, userID, 50); err != nil {
//	        return err
//	    }
//	    return audit.Record(ctx, q, audit.Entry{
//	        SubjectKind: "credit-account",
//	        SubjectID:   userID,
//	        Action:      "debited",
//	        Details:     map[string]any{"amount": 50},
//	    })
//	})
func Record(ctx context.Context, q db.Querier, entry Entry) error {
	entry.SubjectKind = strings.TrimSpace(entry.SubjectKind)
	entry.SubjectID = strings.TrimSpace(entry.SubjectID)
	entry.Action = strings.TrimSpace(entry.Action)
	if entry.SubjectKind == "" || entry.SubjectID == "" || entry.Action == "" {
		return ErrInvalidEntry
	}

	var details []byte
	if entry.Details != nil {
		encoded, err := json.Marshal(entry.Details)
		if err != nil {
			return fmt.Errorf("mirrorstack/audit: details are not serializable: %w", err)
		}
		if len(encoded) > maxDetailBytes {
			return fmt.Errorf("mirrorstack/audit: details are %d bytes; the limit is %d",
				len(encoded), maxDetailBytes)
		}
		details = encoded
	}

	_, err := q.Exec(ctx, `
		INSERT INTO __MODULE_ID___audit_outbox (subject_kind, subject_id, action, details)
		VALUES ($1, $2, $3, $4)`,
		entry.SubjectKind, entry.SubjectID, entry.Action, details)
	return err
}

// EnsureTable creates the outbox if it is missing.
//
// 🔴 IT IS GO DDL RATHER THAN A MIGRATION, AND IT HAS TO BE. An SDK-owned
// numbered migration cannot reach an already-installed module: the runner
// selects migrations above the app's stored watermark, and every shipped module
// is already past 0001 — so a 0000_audit.up.sql sorts below every watermark and
// is skipped forever, giving a two-population schema where the code works in
// some tenants and raises 42P01 in others. The SDK also cannot ship .sql at
// all: the module owns the migration filesystem.
//
// So this runs from the install/upgrade provisioner, outside the version
// sequence entirely — the same shape the contributions store already uses, down
// to the advisory lock that serialises two concurrent installs of the same app.
func EnsureTable(ctx context.Context, q db.Querier) error {
	_, err := q.Exec(ctx, `
		SELECT pg_advisory_xact_lock(hashtext(current_schema() || '.__MODULE_ID___audit_outbox'));

		CREATE TABLE IF NOT EXISTS __MODULE_ID___audit_outbox (
		    id           bigserial PRIMARY KEY,
		    occurred_at  timestamptz NOT NULL DEFAULT now(),
		    subject_kind text NOT NULL,
		    subject_id   text NOT NULL,
		    action       text NOT NULL,
		    details      jsonb,
		    -- NULL until the drain has handed this row to the platform. The
		    -- column is what makes delivery resumable across a restart: an
		    -- undelivered row is simply one that is still NULL.
		    delivered_at timestamptz
		);

		-- The drain reads exactly this: undelivered, oldest first. A partial
		-- index keeps it the size of the BACKLOG rather than of the history,
		-- so a module with a million delivered rows still drains in one seek.
		CREATE INDEX IF NOT EXISTS __MODULE_ID___audit_outbox_pending_idx
		    ON __MODULE_ID___audit_outbox (id) WHERE delivered_at IS NULL;`)
	if err != nil && !pgerr.RelationAlreadyExists(err) {
		return err
	}
	return nil
}
