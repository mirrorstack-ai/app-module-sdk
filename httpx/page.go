// Package httpx is the module SDK's one shape for a paginated list response,
// and the one way to read a page request.
//
// 🔴 WHY THIS EXISTS. Across the shipped modules there were three pagination
// dialects and two byte-identical cursor functions written independently in
// different repos — `auditCursor` in credit and `roleAuditCursor` in
// users-roles are the same function, character for character, and user-core
// runs a third scheme on top. Nothing was wrong with any one of them; there was
// simply no owner for the convention, so every module invented it again. This
// package is that owner.
//
// # Keyset always, offset never
//
// There is no Offset here and there will not be one. Offset pagination is
// wrong under concurrent writes in a way that is invisible in testing: a row
// inserted before the cursor position shifts every later row down, so page 2
// re-shows a row from page 1, and a delete makes page 2 skip one entirely.
// Audit logs and ledgers — which is most of what modules paginate — are
// exactly the append-heavy tables where that happens constantly. A keyset
// cursor names the last row you saw, so concurrent writes cannot move it.
//
// # The cursor is opaque
//
// It is base64url over an internal form, deliberately. A cursor a client can
// read is a cursor a client will parse, and then the encoding cannot change
// without breaking them. Clients get it from NextCursor and hand it back.
package httpx

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Page is the response shape for every paginated list a module serves.
//
// NextCursor is nil — not empty string — when there are no further rows, so
// "no more pages" and "a page boundary that happens to encode as empty" cannot
// be confused. Clients test for null.
type Page[T any] struct {
	// Items is never null on the wire: an empty page marshals as [], because a
	// null that a client must special-case is a bug waiting to be written.
	Items []T `json:"items"`
	// NextCursor is fed back as ?cursor= to fetch the next page. nil means this
	// is the last page.
	NextCursor *string `json:"nextCursor,omitempty"`
	// Truncated marks a page whose ITEMS were cut — a single row too large to
	// return whole, or a result set clipped by a hard cap. It is not the same
	// as "there are more pages"; that is NextCursor. Both can be true.
	Truncated bool `json:"truncated,omitempty"`
	// Note explains a Truncated page in one plain sentence, for a human or a
	// model reading the response. Empty otherwise.
	Note string `json:"note,omitempty"`
}

// NewPage builds a page from rows already trimmed to the requested limit,
// deriving NextCursor from the last row. cursorOf returns the cursor for a row;
// pass nil when the caller knows there are no further pages.
func NewPage[T any](items []T, more bool, cursorOf func(T) string) Page[T] {
	page := Page[T]{Items: items}
	if page.Items == nil {
		page.Items = []T{}
	}
	if more && cursorOf != nil && len(items) > 0 {
		next := cursorOf(items[len(items)-1])
		page.NextCursor = &next
	}
	return page
}

// Truncate marks the page as cut and records why. Returns the page so it can
// be used inline.
func (p Page[T]) Truncate(note string) Page[T] {
	p.Truncated = true
	p.Note = note
	return p
}

// Limit reads ?limit=, clamped to [1, max] and defaulting to def.
//
// A malformed value falls back to def rather than erroring: a page size is a
// hint, and failing a read over one is a worse trade than serving the default.
func Limit(r *http.Request, def, max int) int {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if n < 1 {
		return 1
	}
	if n > max {
		return max
	}
	return n
}

const cursorPrefix = "c1."

// IDCursor is a keyset position on a single monotonically increasing integer
// key — a bigserial audit id, a ledger sequence. Zero means "from the start",
// which is what an absent cursor decodes to.
type IDCursor int64

// String encodes the cursor for the wire. The zero cursor encodes as "",
// so a caller can hand it straight to NextCursor without a special case.
func (c IDCursor) String() string {
	if c == 0 {
		return ""
	}
	return cursorPrefix + base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(int64(c), 10)))
}

// ParseIDCursor reads ?cursor= as an IDCursor.
//
// A cursor that will not decode returns 0 — the first page — rather than an
// error. It is the same answer the client gets for no cursor at all, it cannot
// leak whether some other row exists, and a stale or truncated cursor from an
// old client degrades to "start over" instead of a 400 nobody can act on.
func ParseIDCursor(r *http.Request) IDCursor {
	raw := strings.TrimPrefix(r.URL.Query().Get("cursor"), cursorPrefix)
	if raw == "" {
		return 0
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(string(decoded), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return IDCursor(n)
}

// TimeIDCursor is a keyset position on (timestamp, id) — the ordering almost
// every "newest first" list actually uses.
//
// The ID half is not optional padding: timestamps collide, and two rows written
// in the same microsecond would otherwise make a page boundary ambiguous, so a
// row is either shown twice or skipped. The tiebreak is what makes the keyset
// total.
type TimeIDCursor struct {
	Time time.Time
	ID   string
}

// IsZero reports the "from the start" cursor.
func (c TimeIDCursor) IsZero() bool { return c.ID == "" && c.Time.IsZero() }

// String encodes the cursor for the wire; the zero cursor encodes as "".
func (c TimeIDCursor) String() string {
	if c.IsZero() {
		return ""
	}
	// RFC3339Nano, not Unix seconds: a second-resolution cursor on a table
	// that writes faster than that loses rows at every page boundary.
	raw := c.Time.UTC().Format(time.RFC3339Nano) + "\x1f" + c.ID
	return cursorPrefix + base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// ParseTimeIDCursor reads ?cursor= as a TimeIDCursor. Same
// undecodable-means-first-page rule as ParseIDCursor.
func ParseTimeIDCursor(r *http.Request) TimeIDCursor {
	raw := strings.TrimPrefix(r.URL.Query().Get("cursor"), cursorPrefix)
	if raw == "" {
		return TimeIDCursor{}
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return TimeIDCursor{}
	}
	// A unit separator, not '|': it cannot occur in an RFC3339 timestamp and it
	// cannot occur in a UUID, so a value carrying one cannot split the cursor
	// in the wrong place.
	ts, id, found := strings.Cut(string(decoded), "\x1f")
	if !found || id == "" {
		return TimeIDCursor{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return TimeIDCursor{}
	}
	return TimeIDCursor{Time: parsed, ID: id}
}

// DefaultMaxBytes is the size a tool result should aim to stay under.
//
// It is a guideline, not a limit the SDK enforces: the platform caps a tool
// result at 32 KB when it stores one, and an agent's context is the real
// scarce resource. 16 KB leaves room for the envelope and for the rest of the
// conversation.
const DefaultMaxBytes = 16 << 10

// Cap returns at most as many leading items as fit within maxBytes once
// marshalled, marking the page Truncated when it had to drop any.
//
// 🔴 IT DROPS WHOLE ITEMS, NEVER BYTES. The obvious way to bound a result is
// to marshal it and cut the string, and that produces INVALID JSON — the reader
// gets a parse error instead of data, or worse, a half-written object it
// silently mis-reads. Dropping whole items keeps the result parseable at every
// size, which is what makes truncation something a caller can reason about
// rather than something that breaks them.
//
// The page it returns is self-describing: Truncated says it happened, Note says
// how many were dropped and how to get the rest, and NextCursor (when the
// caller supplies cursorOf) is the position to resume from. A model reading it
// can ask for the next page instead of concluding the list is short.
//
// Binary does not belong here at all. A tool returning bytes should return a
// presigned URL (storage.PresignGet) and let the caller fetch it out of band —
// base64 in a tool result spends the agent's context on data it cannot read.
func Cap[T any](items []T, maxBytes int, cursorOf func(T) string) Page[T] {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	kept := items
	for len(kept) > 0 {
		encoded, err := json.Marshal(kept)
		if err != nil {
			// Unmarshalable items are the caller's bug, not a size problem.
			// Return them and let the marshal fail where it can be diagnosed,
			// rather than silently returning an empty page.
			return NewPage(items, false, nil)
		}
		if len(encoded) <= maxBytes {
			break
		}
		// Halve rather than step: a single oversized item would otherwise make
		// this O(n) marshals of an already-too-large slice.
		kept = kept[:len(kept)/2]
	}

	dropped := len(items) - len(kept)
	page := NewPage(kept, dropped > 0, cursorOf)
	if dropped == 0 {
		return page
	}
	note := fmt.Sprintf("%d of %d items omitted to stay under %d bytes", dropped, len(items), maxBytes)
	if page.NextCursor != nil {
		note += "; pass the cursor to continue"
	} else {
		note += "; narrow the query to see the rest"
	}
	return page.Truncate(note)
}
