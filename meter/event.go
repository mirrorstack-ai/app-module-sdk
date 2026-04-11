package meter

import "time"

// envelopeVersion is the current wire format version. Bump on breaking changes.
const envelopeVersion = 1

// Event is the JSON wire format sent to the platform meter Lambda.
//
// Fields ending in Hint are SDK-asserted values the platform uses for
// debugging and logging but MUST NOT trust for billing attribution. The
// authoritative values come from the AWS invoker identity (context.FunctionArn)
// cross-checked against a platform-owned Lambda→app mapping.
//
// ModuleIDHint is required (no omitempty) even though it is labeled a hint —
// the "Hint" suffix means "SDK-asserted, not authoritative," not "optional."
// AppIDHint is omitempty because system-triggered events may have no app
// context (e.g., cron job running without an AppID in ctx).
type Event struct {
	V              int       `json:"v"`
	EventID        string    `json:"eventId"`
	AppIDHint      string    `json:"appIdHint,omitempty"`
	ModuleIDHint   string    `json:"moduleIdHint"`
	Metric         string    `json:"metric"`
	Value          float64   `json:"value"`
	RecordedAtHint time.Time `json:"recordedAtHint"`
}
