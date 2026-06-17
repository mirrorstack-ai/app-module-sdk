package meter

import "time"

// envelopeVersion is the current wire format version. Bump on breaking changes.
//
// Still v1: the metric KIND lives in the module manifest / platform catalog,
// not on the wire, so adding declaration-first metering required no envelope
// change. (If a wire-incompatible change ever ships post-launch, bump here.)
const envelopeVersion = 1

// Event is the JSON wire format sent to the platform meter ingress.
//
// There is deliberately NO kind field: a metric's kind (counter/gauge) is
// declared once via ms.Meter and travels in the manifest, so the platform's
// catalog is the single authoritative source — a call site can never mislabel
// a metric's semantic on the wire.
//
// Fields ending in Hint are SDK-asserted values the platform uses for
// debugging and logging but MUST NOT trust for billing attribution. The
// authoritative values are re-derived platform-side from the authenticated
// invoker (Axis 2 of the Milestone D design).
//
// ModuleIDHint is required (no omitempty) even though it is labeled a hint —
// the "Hint" suffix means "SDK-asserted, not authoritative," not "optional."
// AppIDHint is omitempty because system-triggered events may have no app
// context (e.g., cron job running without an AppID in ctx).
//
// EventID is the at-least-once retry dedup key: the platform ingest is
// idempotent on it (ON CONFLICT(event_id) DO NOTHING), so the SAME logical
// Record call must reuse the SAME EventID across any transport retry. It is
// minted ONCE per Record call (before the event is handed to the transport)
// and the built Event is reused across any transport retry within that call,
// so a retried delivery is deduped rather than double-counted.
type Event struct {
	V              int       `json:"v"`
	EventID        string    `json:"eventId"`
	AppIDHint      string    `json:"appIdHint,omitempty"`
	ModuleIDHint   string    `json:"moduleIdHint"`
	Metric         string    `json:"metric"`
	Value          float64   `json:"value"`
	RecordedAtHint time.Time `json:"recordedAtHint"`
}
