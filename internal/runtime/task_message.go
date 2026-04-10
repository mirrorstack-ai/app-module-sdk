package runtime

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// TaskMessage is the SQS message envelope for background tasks dispatched
// via Module.RunTask. It mirrors the LambdaRequest shape for credential
// transport but uses a different delivery channel (SQS instead of Lambda
// Invoke). The Signature field provides HMAC integrity verification so the
// task worker can reject forged messages.
type TaskMessage struct {
	TaskID    string          `json:"taskId"`
	Name      string          `json:"name"`
	Payload   json.RawMessage `json:"payload"`
	Resources *Resources      `json:"resources,omitempty"`
	UserID    string          `json:"userId,omitempty"`
	AppID     string          `json:"appId,omitempty"`
	AppRole   string          `json:"appRole,omitempty"`
	AppSchema string          `json:"appSchema,omitempty"`
	Signature string          `json:"sig,omitempty"`
}

// Sign computes an HMAC-SHA256 over the message content (excluding the sig
// field itself) and sets the Signature field. The key should come from
// MS_TASK_SIGNING_KEY. A nil or empty key is a no-op — dev mode skips signing.
func (m *TaskMessage) Sign(key []byte) {
	if len(key) == 0 {
		return
	}
	m.Signature = ""
	m.Signature = computeHMAC(key, m.signingPayload())
}

// Verify checks the HMAC signature. Returns an error if the key is non-empty
// and the signature is missing or invalid. A nil/empty key always passes —
// dev mode skips verification.
func (m *TaskMessage) Verify(key []byte) error {
	if len(key) == 0 {
		return nil
	}
	if m.Signature == "" {
		return fmt.Errorf("mirrorstack: task message missing signature")
	}
	expected := computeHMAC(key, m.signingPayload())
	if !hmac.Equal([]byte(m.Signature), []byte(expected)) {
		return fmt.Errorf("mirrorstack: task message signature mismatch")
	}
	return nil
}

// signingPayload returns a deterministic JSON representation of the message
// fields that are covered by the signature (everything except sig itself).
func (m *TaskMessage) signingPayload() []byte {
	// Use a shadow struct to exclude Signature from marshaling.
	type shadow struct {
		TaskID    string          `json:"taskId"`
		Name      string          `json:"name"`
		Payload   json.RawMessage `json:"payload"`
		Resources *Resources      `json:"resources,omitempty"`
		UserID    string          `json:"userId,omitempty"`
		AppID     string          `json:"appId,omitempty"`
		AppRole   string          `json:"appRole,omitempty"`
		AppSchema string          `json:"appSchema,omitempty"`
	}
	b, _ := json.Marshal(shadow{
		TaskID:    m.TaskID,
		Name:      m.Name,
		Payload:   m.Payload,
		Resources: m.Resources,
		UserID:    m.UserID,
		AppID:     m.AppID,
		AppRole:   m.AppRole,
		AppSchema: m.AppSchema,
	})
	return b
}

func computeHMAC(key, data []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil))
}
