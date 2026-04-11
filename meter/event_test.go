package meter

import (
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
