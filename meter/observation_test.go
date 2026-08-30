package meter

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

const observationEventID = "b9c6a0f0-1234-4abc-8def-0123456789ab"

func TestRecordObservation_PostsCanonicalV2Envelope(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 1, 123456789, time.UTC)
	occurredAt := now.Add(-time.Second).In(time.FixedZone("UTC+8", 8*60*60))
	metadata := json.RawMessage(`{"source":"password","sequence":9007199254740993}`)
	wantMetadata := append(json.RawMessage(nil), metadata...)

	c, capture := newDispatchStub(t, http.StatusAccepted)
	c.now = func() time.Time { return now }
	c.Declare("user-core", DeclFromOptions("users.active", Gauge, BySubject))
	err := c.RecordObservation(appCtx("app_abc"), "users.active", 1, Observation{
		EventID:    observationEventID,
		Subject:    "user-42",
		Metadata:   metadata,
		OccurredAt: occurredAt,
	})
	if err != nil {
		t.Fatalf("RecordObservation: %v", err)
	}

	want := `{"v":2,"eventId":"b9c6a0f0-1234-4abc-8def-0123456789ab","appIdHint":"app_abc","moduleIdHint":"user-core","metric":"users.active","value":1,"subject":"user-42","metadata":{"source":"password","sequence":9007199254740993},"occurredAt":"2026-08-30T12:00:00.123456789Z","recordedAtHint":"2026-08-30T12:00:01.123456789Z"}`
	got := capture.get()
	if string(got.body) != want {
		t.Fatalf("v2 body mismatch\n got: %s\nwant: %s", got.body, want)
	}
	if !bytes.Equal(metadata, wantMetadata) {
		t.Fatalf("RecordObservation mutated caller metadata: got=%s want=%s", metadata, wantMetadata)
	}
}

func TestRecordObservation_PreservesV1WireContract(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 1, 123456789, time.UTC)
	c, capture := newDispatchStub(t, http.StatusAccepted)
	c.now = func() time.Time { return now }
	declareCounter(t, c, "orders.placed")
	if err := c.RecordWithID(appCtx("app_abc"), observationEventID, "orders.placed", 1); err != nil {
		t.Fatalf("RecordWithID: %v", err)
	}
	wantBody := `{"v":1,"eventId":"b9c6a0f0-1234-4abc-8def-0123456789ab","appIdHint":"app_abc","moduleIdHint":"media","metric":"orders.placed","value":1,"recordedAtHint":"2026-08-30T12:00:01.123456789Z"}`
	if got := string(capture.get().body); got != wantBody {
		t.Fatalf("v1 body changed\n got: %s\nwant: %s", got, wantBody)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(capture.get().body, &fields); err != nil {
		t.Fatalf("decode v1 body: %v", err)
	}
	wantKeys := map[string]bool{
		"v": true, "eventId": true, "appIdHint": true, "moduleIdHint": true,
		"metric": true, "value": true, "recordedAtHint": true,
	}
	if len(fields) != len(wantKeys) {
		t.Fatalf("v1 keys = %v, want exactly %v", keys(fields), wantKeys)
	}
	for key := range fields {
		if !wantKeys[key] {
			t.Fatalf("v1 wire gained unexpected key %q: %s", key, capture.get().body)
		}
	}
	if string(fields["v"]) != "1" {
		t.Fatalf("v1 version = %s, want 1", fields["v"])
	}
}

func TestRecordObservation_ValidatesSubjectBeforeTransport(t *testing.T) {
	tests := []struct {
		name    string
		subject string
		valid   bool
	}{
		{name: "empty ordinary subject", subject: "", valid: true},
		{name: "opaque unicode", subject: "opaque:使用者/42", valid: true},
		{name: "256 bytes", subject: strings.Repeat("x", maxSubjectBytes), valid: true},
		{name: "257 bytes", subject: strings.Repeat("x", maxSubjectBytes+1)},
		{name: "control", subject: "user\n42"},
		{name: "invalid utf8", subject: string([]byte{0xff})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
			c, capture := newDispatchStub(t, http.StatusAccepted)
			c.now = func() time.Time { return now }
			declareCounter(t, c, "auth.signin")
			err := c.RecordObservation(appCtx("app_abc"), "auth.signin", 1, Observation{
				EventID: observationEventID, Subject: test.subject, OccurredAt: now,
			})
			if test.valid && err != nil {
				t.Fatalf("valid subject rejected: %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("invalid subject accepted")
			}
			wantHits := 0
			if test.valid {
				wantHits = 1
			}
			if got := capture.get().hits; got != wantHits {
				t.Fatalf("transport hits = %d, want %d", got, wantHits)
			}
		})
	}
}

func TestRecordObservation_SubjectKeyedMetricRequiresSubjectAndV2(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	c, capture := newDispatchStub(t, http.StatusAccepted)
	c.now = func() time.Time { return now }
	c.Declare("user-core", DeclFromOptions("users.active", Gauge, BySubject))

	if err := c.Record(appCtx("app_abc"), "users.active", 1); err == nil || !strings.Contains(err.Error(), "RecordObservation") {
		t.Fatalf("Record on subject-keyed metric error = %v, want RecordObservation guidance", err)
	}
	if err := c.RecordWithID(appCtx("app_abc"), observationEventID, "users.active", 1); err == nil || !strings.Contains(err.Error(), "RecordObservation") {
		t.Fatalf("RecordWithID on subject-keyed metric error = %v, want RecordObservation guidance", err)
	}
	if err := c.RecordObservation(appCtx("app_abc"), "users.active", 1, Observation{
		EventID: observationEventID, OccurredAt: now,
	}); err == nil || !strings.Contains(err.Error(), "subject") {
		t.Fatalf("empty keyed subject error = %v, want subject validation", err)
	}
	if got := capture.get().hits; got != 0 {
		t.Fatalf("invalid keyed calls reached transport %d times", got)
	}
}

func TestRecordObservation_LeavesOccurrenceWindowToPlatform(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		occurredAt time.Time
		wantUTC    time.Time
		valid      bool
	}{
		{name: "required", occurredAt: time.Time{}},
		{
			name:       "far future reaches authoritative platform",
			occurredAt: now.Add(365 * 24 * time.Hour).In(time.FixedZone("UTC+8", 8*60*60)),
			wantUTC:    now.Add(365 * 24 * time.Hour),
			valid:      true,
		},
		{
			name:       "far past reaches authoritative platform",
			occurredAt: now.Add(-365 * 24 * time.Hour).In(time.FixedZone("UTC-7", -7*60*60)),
			wantUTC:    now.Add(-365 * 24 * time.Hour),
			valid:      true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c, capture := newDispatchStub(t, http.StatusAccepted)
			c.now = func() time.Time { return now }
			declareCounter(t, c, "auth.signin")
			err := c.RecordObservation(appCtx("app_abc"), "auth.signin", 1, Observation{
				EventID: observationEventID, OccurredAt: test.occurredAt,
			})
			if test.valid && err != nil {
				t.Fatalf("SDK rejected a platform-owned occurrence-window decision: %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("zero occurrence accepted")
			}
			wantHits := 0
			if test.valid {
				wantHits = 1
				var body observationEvent
				if err := json.Unmarshal(capture.get().body, &body); err != nil {
					t.Fatalf("decode observation body: %v", err)
				}
				if !body.OccurredAt.Equal(test.wantUTC) || body.OccurredAt.Location() != time.UTC {
					t.Fatalf("occurredAt = %s (%s), want UTC %s", body.OccurredAt, body.OccurredAt.Location(), test.wantUTC)
				}
			}
			if got := capture.get().hits; got != wantHits {
				t.Fatalf("transport hits = %d, want %d", got, wantHits)
			}
		})
	}
}

func TestRecordObservation_ValidatesMetadataWithoutNormalizingIt(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	valid := []json.RawMessage{
		nil,
		json.RawMessage(`{}`),
		json.RawMessage(`{"same":1.0,"large":9007199254740993}`),
		json.RawMessage(`{"` + "a" + strings.Repeat("x", maxMetadataKeyBytes-1) + `":true}`),
		json.RawMessage(`{"value":"` + strings.Repeat("x", maxMetadataStringBytes) + `"}`),
		json.RawMessage(`{"value":` + strings.Repeat("9", maxMetadataNumberBytes) + `}`),
		json.RawMessage(`{"a":{"b":{"c":1}},"items":[` + strings.TrimSuffix(strings.Repeat("0,", maxMetadataArrayItems), ",") + `]}`),
		metadataMembers(maxMetadataMembers),
		metadataObjectOfSize(t, maxMetadataBytes),
	}
	for i, metadata := range valid {
		t.Run("valid-"+string(rune('a'+i)), func(t *testing.T) {
			c, capture := newDispatchStub(t, http.StatusAccepted)
			c.now = func() time.Time { return now }
			declareCounter(t, c, "auth.signin")
			before := append(json.RawMessage(nil), metadata...)
			err := c.RecordObservation(appCtx("app_abc"), "auth.signin", 1, Observation{
				EventID: observationEventID, Metadata: metadata, OccurredAt: now,
			})
			if err != nil {
				t.Fatalf("valid metadata rejected: %v", err)
			}
			if !bytes.Equal(metadata, before) {
				t.Fatalf("caller metadata mutated: got=%s want=%s", metadata, before)
			}
			if len(metadata) > 0 && !bytes.Contains(capture.get().body, append([]byte(`"metadata":`), metadata...)) {
				t.Fatalf("metadata bytes were normalized instead of embedded: metadata=%s body=%s", metadata, capture.get().body)
			}
		})
	}

	invalid := map[string]json.RawMessage{
		"raw too large":    append(metadataObjectOfSize(t, maxMetadataBytes), ' '),
		"root array":       json.RawMessage(`[]`),
		"trailing value":   json.RawMessage(`{} {}`),
		"invalid utf8":     json.RawMessage{'{', '"', 'a', '"', ':', '"', 0xff, '"', '}'},
		"duplicate key":    json.RawMessage(`{"a":1,"a":2}`),
		"bad key":          json.RawMessage(`{"bad key":1}`),
		"key starts digit": json.RawMessage(`{"1bad":1}`),
		"key too long":     json.RawMessage(`{"` + "a" + strings.Repeat("x", maxMetadataKeyBytes) + `":1}`),
		"too many members": metadataMembers(maxMetadataMembers + 1),
		"too deep":         json.RawMessage(`{"a":{"b":{"c":{"d":1}}}}`),
		"array too long":   json.RawMessage(`{"items":[` + strings.TrimSuffix(strings.Repeat("0,", maxMetadataArrayItems+1), ",") + `]}`),
		"string too long":  json.RawMessage(`{"value":"` + strings.Repeat("x", maxMetadataStringBytes+1) + `"}`),
		"number too long":  json.RawMessage(`{"value":` + strings.Repeat("9", maxMetadataNumberBytes+1) + `}`),
		"number nonfinite": json.RawMessage(`{"value":1e9999}`),
	}
	for name, metadata := range invalid {
		t.Run(name, func(t *testing.T) {
			c, capture := newDispatchStub(t, http.StatusAccepted)
			c.now = func() time.Time { return now }
			declareCounter(t, c, "auth.signin")
			err := c.RecordObservation(appCtx("app_abc"), "auth.signin", 1, Observation{
				EventID: observationEventID, Metadata: metadata, OccurredAt: now,
			})
			if err == nil {
				t.Fatalf("invalid metadata accepted: %s", metadata)
			}
			if got := capture.get().hits; got != 0 {
				t.Fatalf("invalid metadata reached transport %d times", got)
			}
		})
	}
}

func TestRecordObservation_RejectsInvalidEventIDBeforeTransport(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	c, capture := newDispatchStub(t, http.StatusAccepted)
	c.now = func() time.Time { return now }
	declareCounter(t, c, "auth.signin")
	for _, eventID := range []string{"", "not-a-uuid", "B9C6A0F0-1234-4ABC-8DEF-0123456789AB", "00000000-0000-0000-0000-000000000000"} {
		if err := c.RecordObservation(appCtx("app_abc"), "auth.signin", 1, Observation{
			EventID: eventID, OccurredAt: now,
		}); err == nil {
			t.Errorf("event ID %q accepted", eventID)
		}
	}
	if got := capture.get().hits; got != 0 {
		t.Fatalf("invalid event IDs reached transport %d times", got)
	}
}

func metadataObjectOfSize(t *testing.T, size int) json.RawMessage {
	t.Helper()
	const members = 8
	overhead := 2 + (members - 1)
	for i := 0; i < members; i++ {
		overhead += len(`"a0":""`)
	}
	payloadBytes := size - overhead
	if payloadBytes < 0 || payloadBytes > members*maxMetadataStringBytes {
		t.Fatalf("cannot build %d-byte metadata fixture", size)
	}
	var body strings.Builder
	body.Grow(size)
	body.WriteByte('{')
	for i := 0; i < members; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		body.WriteString(`"a`)
		body.WriteByte(byte('0' + i))
		body.WriteString(`":"`)
		part := payloadBytes / (members - i)
		payloadBytes -= part
		body.WriteString(strings.Repeat("x", part))
		body.WriteByte('"')
	}
	body.WriteByte('}')
	if body.Len() != size {
		t.Fatalf("metadata fixture size = %d, want %d", body.Len(), size)
	}
	return json.RawMessage(body.String())
}

func metadataMembers(count int) json.RawMessage {
	var body strings.Builder
	body.WriteByte('{')
	for i := 0; i < count; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		body.WriteString(`"a`)
		body.WriteString(strings.Repeat("x", i/26))
		body.WriteByte(byte('a' + i%26))
		body.WriteString(`":1`)
	}
	body.WriteByte('}')
	return json.RawMessage(body.String())
}
