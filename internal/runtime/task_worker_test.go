package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestSafeRun_ReturnsNil(t *testing.T) {
	t.Parallel()
	err := safeRun(context.Background(), func(ctx context.Context, p json.RawMessage) error {
		return nil
	}, nil)
	if err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestSafeRun_ReturnsError(t *testing.T) {
	t.Parallel()
	err := safeRun(context.Background(), func(ctx context.Context, p json.RawMessage) error {
		return fmt.Errorf("boom")
	}, nil)
	if err == nil || err.Error() != "boom" {
		t.Errorf("expected 'boom', got %v", err)
	}
}

func TestSafeRun_RecoversPanic(t *testing.T) {
	t.Parallel()
	err := safeRun(context.Background(), func(ctx context.Context, p json.RawMessage) error {
		panic("handler crashed")
	}, nil)
	if err == nil {
		t.Fatal("expected error from panic recovery")
	}
	if err.Error() != "task handler panicked: handler crashed" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSafeRun_RespectsTimeout(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := safeRun(ctx, func(ctx context.Context, p json.RawMessage) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return nil
		}
	}, nil)
	if err != context.DeadlineExceeded {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
}

// --- Security: cross-tenant attack tests ---

func TestProcessMessage_RejectsForgedSignature(t *testing.T) {
	t.Parallel()

	key := []byte("real-signing-key")

	// Attacker forges a message with a different tenant's schema
	forged := TaskMessage{
		TaskID:    "forged-1",
		Name:      "work",
		AppSchema: "app_victim",
		AppRole:   "admin",
	}
	forged.Sign([]byte("attacker-key")) // signed with wrong key

	body, _ := json.Marshal(forged)

	// Verify should fail
	var parsed TaskMessage
	json.Unmarshal(body, &parsed)
	if err := parsed.Verify(key); err == nil {
		t.Error("forged message should be rejected by HMAC verification")
	}
}

func TestProcessMessage_RejectsUnsignedWhenKeySet(t *testing.T) {
	t.Parallel()

	key := []byte("signing-key")

	msg := TaskMessage{
		TaskID:    "unsigned-1",
		Name:      "work",
		AppSchema: "app_target",
	}
	// Deliberately not signed

	if err := msg.Verify(key); err == nil {
		t.Error("unsigned message should be rejected when signing key is configured")
	}
}

func TestProcessMessage_RejectsInvalidAppSchema(t *testing.T) {
	t.Parallel()

	// Even if HMAC passes, InjectResources rejects bad schemas
	_, err := InjectResources(context.Background(), InjectParams{
		AppSchema: `app_x; DROP TABLE users;--`,
		AppRole:   "admin",
	})
	if err == nil {
		t.Error("SQL injection in AppSchema should be rejected by InjectResources")
	}
}

func TestProcessMessage_RejectsUnknownRole(t *testing.T) {
	t.Parallel()

	_, err := InjectResources(context.Background(), InjectParams{
		AppSchema: "app_abc",
		AppRole:   "superadmin",
	})
	if err == nil {
		t.Error("unknown AppRole should be rejected by InjectResources")
	}
}

func TestProcessMessage_RejectsTamperedAppSchema(t *testing.T) {
	t.Parallel()

	key := []byte("signing-key")

	// Sign a legitimate message
	msg := TaskMessage{
		TaskID:    "t1",
		Name:      "work",
		AppSchema: "app_legitimate",
		AppRole:   "member",
	}
	msg.Sign(key)

	// Tamper with AppSchema after signing (cross-tenant attack)
	msg.AppSchema = "app_victim"

	if err := msg.Verify(key); err == nil {
		t.Error("tampered AppSchema should be detected by HMAC verification")
	}
}
