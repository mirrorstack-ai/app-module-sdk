package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/mirrorstack-ai/app-module-sdk/internal/auditoutbox"
	"github.com/mirrorstack-ai/app-module-sdk/internal/invocationwire"
)

const (
	auditDrainBatch      = 16
	auditDrainTimeout    = 30 * time.Second
	auditCommitTimeout   = 5 * time.Second
	auditDeliveryTimeout = 8 * time.Second
	auditFinalizeTimeout = 2 * time.Second
	auditLeaseDuration   = 2 * time.Minute
	auditMaximumAttempts = 8
	auditMaximumBackoff  = 5 * time.Minute
)

type auditIngressEnvelope struct {
	V           int             `json:"v"`
	EventID     string          `json:"eventId"`
	OccurredAt  time.Time       `json:"occurredAt"`
	SubjectKind string          `json:"subjectKind"`
	SubjectID   string          `json:"subjectId"`
	Action      string          `json:"action"`
	Details     json.RawMessage `json:"details,omitempty"`
}

type auditDisposition uint8

const (
	auditAcknowledge auditDisposition = iota
	auditRetry
	auditQuarantine
)

// DrainAudit forwards at most one bounded batch from the current app's audit
// outbox. Rows are leased with a rotating fence, so callers may safely run this
// concurrently from post-commit hooks, cron handlers, and maintenance jobs.
//
// Delivery is at-least-once. API Platform de-duplicates EventID within the
// invocation-proven app and module stream.
func (m *Module) DrainAudit(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, auditDrainTimeout)
	defer cancel()

	q, release, err := m.DB(ctx)
	if err != nil {
		return fmt.Errorf("mirrorstack: Audit: open outbox: %w", err)
	}
	defer release()

	var deliveryErrors []error
	for processed := 0; processed < auditDrainBatch && ctx.Err() == nil; processed++ {
		deliveries, claimErr := auditoutbox.Claim(ctx, q, 1, time.Now().Add(auditLeaseDuration))
		if claimErr != nil {
			deliveryErrors = append(deliveryErrors,
				fmt.Errorf("mirrorstack: Audit: claim outbox: %w", claimErr))
			break
		}
		if len(deliveries) == 0 {
			break
		}
		delivery := deliveries[0]
		disposition, code, deliveryErr := deliverAudit(ctx, delivery)
		if disposition == auditRetry && delivery.Attempts >= auditMaximumAttempts {
			disposition = auditQuarantine
			code = "attempts_exhausted_" + code
		}

		finalizeCtx, finalizeCancel := context.WithTimeout(context.WithoutCancel(ctx), auditFinalizeTimeout)
		switch disposition {
		case auditAcknowledge:
			err = auditoutbox.Acknowledge(finalizeCtx, q, delivery.ID, delivery.LeaseToken)
		case auditRetry:
			err = auditoutbox.Retry(finalizeCtx, q, delivery.ID, delivery.LeaseToken, code,
				time.Now().Add(auditRetryBackoff(delivery.Attempts)))
		case auditQuarantine:
			err = auditoutbox.Quarantine(finalizeCtx, q, delivery.ID, delivery.LeaseToken, code)
		}
		finalizeCancel()
		if err != nil {
			deliveryErrors = append(deliveryErrors,
				fmt.Errorf("event %s finalize: %w", delivery.EventID, err))
		}
		if deliveryErr != nil {
			deliveryErrors = append(deliveryErrors,
				fmt.Errorf("event %s: %w", delivery.EventID, deliveryErr))
		}
		if ctx.Err() != nil {
			break
		}
	}
	return errors.Join(deliveryErrors...)
}

func deliverAudit(ctx context.Context, delivery auditoutbox.Delivery) (auditDisposition, string, error) {
	inv, err := invocationwire.Parse(delivery.Proof)
	if err != nil {
		return auditQuarantine, "invalid_invocation_proof", errors.New("stored invocation proof is invalid")
	}
	header, err := invocationwire.EncodeHeader(delivery.Proof)
	if err != nil {
		return auditQuarantine, "invalid_invocation_proof", errors.New("stored invocation proof is invalid")
	}
	secret, err := outboundServiceSecret(ctx, "Audit")
	if err != nil {
		return auditRetry, "authority_unavailable", err
	}
	if secret == "" {
		return auditRetry, "authority_unavailable", errors.New("audit delivery authority is unavailable")
	}

	deliveryCtx, cancel := context.WithTimeout(ctx, auditDeliveryTimeout)
	defer cancel()
	err = postDispatchJSON(deliveryCtx, "ms.Audit",
		dispatchBaseFor(deliveryCtx)+"/apps/"+url.PathEscape(inv.App.ID)+"/audit",
		inv.App.ID,
		auditIngressEnvelope{
			V: 1, EventID: delivery.EventID, OccurredAt: delivery.OccurredAt.UTC(),
			SubjectKind: delivery.SubjectKind, SubjectID: delivery.SubjectID,
			Action: delivery.Action, Details: delivery.Details,
		},
		map[string]string{
			"X-MS-Service-Secret": secret,
			invocationwire.Header: header,
		})
	if err == nil {
		return auditAcknowledge, "", nil
	}

	var responseErr *dispatchResponseError
	if !errors.As(err, &responseErr) {
		return auditRetry, "transport_unavailable", errors.New("audit ingress transport unavailable")
	}
	code := fmt.Sprintf("http_%d", responseErr.statusCode)
	switch responseErr.statusCode {
	case 400, 409, 413, 422:
		return auditQuarantine, code, fmt.Errorf("audit ingress rejected event with status %d", responseErr.statusCode)
	default:
		return auditRetry, code, fmt.Errorf("audit ingress returned status %d", responseErr.statusCode)
	}
}

func auditRetryBackoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	if attempts > 8 {
		attempts = 8
	}
	delay := time.Second * time.Duration(1<<attempts)
	if delay > auditMaximumBackoff {
		return auditMaximumBackoff
	}
	return delay
}

// drainAuditAfterCommit is deliberately best-effort: the mutation is already
// committed together with its outbox row. A delivery outage must leave that
// durable row retryable, not turn a successful mutation into a reported error.
func (m *Module) drainAuditAfterCommit(ctx context.Context) {
	drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditCommitTimeout)
	defer cancel()
	if err := m.DrainAudit(drainCtx); err != nil {
		m.logger.Printf("audit delivery deferred: %v", err)
	}
}

// DrainAudit forwards one bounded audit-outbox batch for the default module.
func DrainAudit(ctx context.Context) error {
	return mustDefault("DrainAudit").DrainAudit(ctx)
}
