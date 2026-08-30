package meter

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestEvent_JSONRoundTrip(t *testing.T) {
	t.Parallel()
	now := time.Unix(1712888400, 0).UTC()
	in := Event{
		V:              1,
		EventID:        "b9c6a0f0-1234-4abc-8def-0123456789ab",
		AppIDHint:      "app_abc",
		ModuleIDHint:   "media",
		Metric:         "transcode.minutes",
		Value:          12.5,
		RecordedAtHint: now,
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Confirm Hint suffix is visible on the wire.
	raw := string(b)
	for _, key := range []string{`"appIdHint"`, `"moduleIdHint"`, `"recordedAtHint"`, `"v":1`, `"eventId"`} {
		if !strings.Contains(raw, key) {
			t.Errorf("wire format missing %s: %s", key, raw)
		}
	}

	var out Event
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.EventID != in.EventID || out.ModuleIDHint != in.ModuleIDHint || out.Metric != in.Metric || out.Value != in.Value {
		t.Errorf("roundtrip mismatch: %+v", out)
	}
}

func TestEvent_EmptyAppIDHintOmitted(t *testing.T) {
	t.Parallel()
	e := Event{V: 1, EventID: "x", ModuleIDHint: "media", Metric: "m", Value: 1, RecordedAtHint: time.Now()}
	b, _ := json.Marshal(e)
	if strings.Contains(string(b), "appIdHint") {
		t.Errorf("empty appIdHint should be omitted, got: %s", string(b))
	}
}

func TestEvent_V2ObservationFieldsAreEmbeddedObjects(t *testing.T) {
	t.Parallel()
	metadata := json.RawMessage(`{"source":"password","sequence":9007199254740993}`)
	e := observationEvent{
		V:              2,
		EventID:        "b9c6a0f0-1234-4abc-8def-0123456789ab",
		AppIDHint:      "app_abc",
		ModuleIDHint:   "user-core",
		Metric:         "users.active",
		Value:          1,
		Subject:        "user-42",
		Metadata:       metadata,
		OccurredAt:     time.Date(2026, 8, 30, 11, 59, 59, 0, time.UTC),
		RecordedAtHint: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
	}

	body, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"eventId"`, `"appIdHint"`, `"moduleIdHint"`, `"subject"`, `"metadata"`, `"occurredAt"`, `"recordedAtHint"`} {
		if !bytes.Contains(body, []byte(key)) {
			t.Errorf("v2 wire format missing %s: %s", key, body)
		}
	}
	if bytes.Contains(body, []byte(`"metadata":"`)) {
		t.Fatalf("metadata was encoded as a string instead of an object: %s", body)
	}
	if !bytes.Contains(body, append([]byte(`"metadata":`), metadata...)) {
		t.Fatalf("metadata number spelling/order changed: body=%s metadata=%s", body, metadata)
	}
}
