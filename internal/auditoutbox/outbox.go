// Package auditoutbox owns the SDK-private lease and fence mechanics for the
// public audit package's per-app table. Module authors use audit.Record and
// ms.DrainAudit; exposing this layer would invite competing drainers.
package auditoutbox

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/mirrorstack-ai/app-module-sdk/db"
)

const maxClaimLimit = 100

// ErrLeaseLost means a newer claim owns the row. A stale worker must not
// acknowledge, retry, or quarantine it.
var ErrLeaseLost = errors.New("mirrorstack/audit: outbox lease was fenced")

// Delivery is one privately-provenanced outbox row leased for forwarding.
// Proof is exact canonical invocation JSON and must only be placed back into
// the platform invocation header; it must never be logged or exposed to module
// code.
type Delivery struct {
	ID          int64
	EventID     string
	OccurredAt  time.Time
	SubjectKind string
	SubjectID   string
	Action      string
	Details     json.RawMessage
	Proof       []byte
	Attempts    int
	LeaseToken  string
}

// Claim leases the oldest ready rows with SKIP LOCKED. Expired claims are
// reclaimable and receive a new token, fencing the old worker.
func Claim(ctx context.Context, q db.Querier, limit int, leaseUntil time.Time) ([]Delivery, error) {
	if limit <= 0 || limit > maxClaimLimit {
		limit = maxClaimLimit
	}
	rows, err := q.Query(ctx, `
		WITH candidates AS (
		    SELECT id
		    FROM __MODULE_ID___audit_outbox
		    WHERE delivered_at IS NULL
		      AND quarantined_at IS NULL
		      AND available_at <= clock_timestamp()
		      AND (lease_expires_at IS NULL OR lease_expires_at < clock_timestamp())
		    ORDER BY available_at, id
		    FOR UPDATE SKIP LOCKED
		    LIMIT $1
		), leased AS (
		    UPDATE __MODULE_ID___audit_outbox AS event
		    SET attempts = event.attempts + 1,
		        lease_token = gen_random_uuid(),
		        lease_expires_at = $2
		    FROM candidates
		    WHERE event.id = candidates.id
		    RETURNING event.id, event.event_id, event.occurred_at,
		              event.subject_kind, event.subject_id, event.action,
		              event.details, event.invocation_proof, event.attempts,
		              event.lease_token
		)
		SELECT id, event_id, occurred_at, subject_kind, subject_id, action,
		       details, invocation_proof, attempts, lease_token
		FROM leased
		ORDER BY id`, limit, leaseUntil.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	deliveries := make([]Delivery, 0, limit)
	for rows.Next() {
		var delivery Delivery
		if err := rows.Scan(
			&delivery.ID, &delivery.EventID, &delivery.OccurredAt,
			&delivery.SubjectKind, &delivery.SubjectID, &delivery.Action,
			&delivery.Details, &delivery.Proof, &delivery.Attempts,
			&delivery.LeaseToken,
		); err != nil {
			return nil, err
		}
		delivery.Proof = append([]byte(nil), delivery.Proof...)
		delivery.Details = append(json.RawMessage(nil), delivery.Details...)
		deliveries = append(deliveries, delivery)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return deliveries, nil
}

// Acknowledge marks a lease delivered only when its rotating token still owns
// the row.
func Acknowledge(ctx context.Context, q db.Querier, id int64, token string) error {
	result, err := q.Exec(ctx, `
		UPDATE __MODULE_ID___audit_outbox
		SET delivered_at = clock_timestamp(),
		    lease_token = NULL,
		    lease_expires_at = NULL,
		    last_error_code = NULL
		WHERE id = $1 AND delivered_at IS NULL AND quarantined_at IS NULL
		  AND lease_token = $2`, id, token)
	return fencedMutation(result.RowsAffected(), err)
}

// Retry releases a lease at a bounded future time.
func Retry(ctx context.Context, q db.Querier, id int64, token, code string, availableAt time.Time) error {
	result, err := q.Exec(ctx, `
		UPDATE __MODULE_ID___audit_outbox
		SET available_at = $3,
		    lease_token = NULL,
		    lease_expires_at = NULL,
		    last_error_code = $4
		WHERE id = $1 AND delivered_at IS NULL AND quarantined_at IS NULL
		  AND lease_token = $2`, id, token, availableAt.UTC(), boundedErrorCode(code))
	return fencedMutation(result.RowsAffected(), err)
}

// Quarantine makes a permanently invalid or exhausted row terminal without
// deleting the evidence an operator needs to inspect it.
func Quarantine(ctx context.Context, q db.Querier, id int64, token, code string) error {
	result, err := q.Exec(ctx, `
		UPDATE __MODULE_ID___audit_outbox
		SET quarantined_at = clock_timestamp(),
		    lease_token = NULL,
		    lease_expires_at = NULL,
		    last_error_code = $3
		WHERE id = $1 AND delivered_at IS NULL AND quarantined_at IS NULL
		  AND lease_token = $2`, id, token, boundedErrorCode(code))
	return fencedMutation(result.RowsAffected(), err)
}

func fencedMutation(rows int64, err error) error {
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrLeaseLost
	}
	return nil
}

func boundedErrorCode(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return "delivery_failed"
	}
	if len(code) > 96 {
		return code[:96]
	}
	return code
}
