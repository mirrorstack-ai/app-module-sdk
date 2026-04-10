package runtime

import (
	"encoding/json"
	"testing"
)

func TestTaskMessage_JSONRoundTrip(t *testing.T) {
	t.Parallel()

	msg := TaskMessage{
		TaskID:    "tid-123",
		Name:      "transcode",
		Payload:   json.RawMessage(`{"videoId":"v1"}`),
		UserID:    "u1",
		AppID:     "a1",
		AppRole:   "admin",
		AppSchema: "app_abc",
	}

	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got TaskMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.TaskID != "tid-123" || got.Name != "transcode" || got.AppSchema != "app_abc" {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
	if string(got.Payload) != `{"videoId":"v1"}` {
		t.Errorf("payload = %s, want {\"videoId\":\"v1\"}", string(got.Payload))
	}
}

func TestTaskMessage_SignVerify(t *testing.T) {
	t.Parallel()

	key := []byte("test-signing-key")
	msg := TaskMessage{
		TaskID:  "tid-1",
		Name:    "work",
		Payload: json.RawMessage(`{}`),
	}

	msg.Sign(key)
	if msg.Signature == "" {
		t.Fatal("Sign should set Signature")
	}

	if err := msg.Verify(key); err != nil {
		t.Errorf("Verify should pass: %v", err)
	}
}

func TestTaskMessage_VerifyRejectsTampered(t *testing.T) {
	t.Parallel()

	key := []byte("test-signing-key")
	msg := TaskMessage{
		TaskID:  "tid-1",
		Name:    "work",
		Payload: json.RawMessage(`{}`),
	}
	msg.Sign(key)

	msg.Name = "tampered" // modify after signing
	if err := msg.Verify(key); err == nil {
		t.Error("Verify should reject tampered message")
	}
}

func TestTaskMessage_VerifyRejectsWrongKey(t *testing.T) {
	t.Parallel()

	msg := TaskMessage{TaskID: "tid-1", Name: "work", Payload: json.RawMessage(`{}`)}
	msg.Sign([]byte("key-a"))

	if err := msg.Verify([]byte("key-b")); err == nil {
		t.Error("Verify should reject wrong key")
	}
}

func TestTaskMessage_SignVerify_EmptyKeySkips(t *testing.T) {
	t.Parallel()

	msg := TaskMessage{TaskID: "tid-1", Name: "work"}
	msg.Sign(nil) // no-op
	if msg.Signature != "" {
		t.Error("Sign with nil key should not set signature")
	}
	if err := msg.Verify(nil); err != nil {
		t.Errorf("Verify with nil key should pass: %v", err)
	}
}

func TestTaskMessage_VerifyRejectsMissingSig(t *testing.T) {
	t.Parallel()

	msg := TaskMessage{TaskID: "tid-1", Name: "work"}
	if err := msg.Verify([]byte("key")); err == nil {
		t.Error("Verify should reject missing signature when key is set")
	}
}
