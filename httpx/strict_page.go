package httpx

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	scopedCursorPrefix = "cs1."
	maxScopedCursorLen = 512
)

var (
	// ErrInvalidQuery means a strict query was malformed, contained an
	// undeclared key, or repeated a key.
	ErrInvalidQuery = errors.New("httpx: invalid query")
	// ErrInvalidLimit means an explicit strict page limit was malformed or
	// outside the configured bounds.
	ErrInvalidLimit = errors.New("httpx: invalid limit")
	// ErrInvalidCursor means a scoped cursor was malformed, stale for another
	// filter scope, or did not name a valid time-and-ID position.
	ErrInvalidCursor = errors.New("httpx: invalid cursor")
)

// ParseStrictQuery decodes a raw query and rejects unknown or repeated keys.
// accepted lists the complete query contract for the endpoint.
func ParseStrictQuery(raw string, accepted ...string) (url.Values, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return nil, ErrInvalidQuery
	}
	allowed := make(map[string]struct{}, len(accepted))
	for _, key := range accepted {
		allowed[key] = struct{}{}
	}
	for key, entries := range values {
		if _, ok := allowed[key]; !ok || len(entries) != 1 {
			return nil, ErrInvalidQuery
		}
	}
	return values, nil
}

// ParseStrictLimit validates a caller-provided page size. Empty raw uses
// defaultLimit; explicit values must be within [1, maxLimit]. It panics for an
// invalid server-side configuration because that is a startup/programming bug,
// not a request error.
func ParseStrictLimit(raw string, defaultLimit, maxLimit int) (int, error) {
	if defaultLimit < 1 || maxLimit < defaultLimit {
		panic("httpx: invalid strict limit configuration")
	}
	if raw == "" {
		return defaultLimit, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > maxLimit {
		return 0, ErrInvalidLimit
	}
	return limit, nil
}

// CursorScope is the opaque identity of one paginated resource and its
// normalized filters. Cursors are accepted only inside the scope that created
// them, preventing a boundary from one filtered list being replayed against
// another.
type CursorScope struct {
	digest string
}

// NewCursorScope builds an unambiguous scope for resource and its normalized
// filters. Filter values are length-prefixed so distinct tuples cannot collide.
// resource must be a stable, non-empty endpoint/resource name.
func NewCursorScope(resource string, normalizedFilters ...string) CursorScope {
	if resource == "" {
		panic("httpx: cursor scope requires a resource")
	}
	var value strings.Builder
	value.WriteString(resource)
	for _, filter := range normalizedFilters {
		_, _ = fmt.Fprintf(&value, "\x1f%d:", len(filter))
		value.WriteString(filter)
	}
	sum := sha256.Sum256([]byte(value.String()))
	return CursorScope{digest: hex.EncodeToString(sum[:16])}
}

type scopedTimeIDCursor struct {
	Time  time.Time `json:"time"`
	ID    string    `json:"id"`
	Scope string    `json:"scope"`
}

// ScopedString encodes the cursor with its filter scope. The zero cursor
// encodes as "", matching TimeIDCursor.String.
func (c TimeIDCursor) ScopedString(scope CursorScope) string {
	if c.IsZero() {
		return ""
	}
	if scope.digest == "" {
		panic("httpx: ScopedString requires a CursorScope from NewCursorScope")
	}
	payload, err := json.Marshal(scopedTimeIDCursor{
		Time:  c.Time.UTC(),
		ID:    c.ID,
		Scope: scope.digest,
	})
	if err != nil {
		return ""
	}
	return scopedCursorPrefix + base64.RawURLEncoding.EncodeToString(payload)
}

// ParseScopedTimeIDCursor decodes a cursor for scope. Empty raw means the
// first page. Cursors carry a cs1. version prefix. All malformed, oversized,
// unknown-version, or cross-scope values return ErrInvalidCursor.
func ParseScopedTimeIDCursor(raw string, scope CursorScope) (TimeIDCursor, error) {
	if raw == "" {
		return TimeIDCursor{}, nil
	}
	if scope.digest == "" || len(raw) > maxScopedCursorLen {
		return TimeIDCursor{}, ErrInvalidCursor
	}
	if !strings.HasPrefix(raw, scopedCursorPrefix) {
		return TimeIDCursor{}, ErrInvalidCursor
	}
	encoded := strings.TrimPrefix(raw, scopedCursorPrefix)
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return TimeIDCursor{}, ErrInvalidCursor
	}
	var cursor scopedTimeIDCursor
	if err := UnmarshalStrict(payload, &cursor); err != nil || cursor.Time.IsZero() ||
		cursor.ID == "" || cursor.Scope != scope.digest {
		return TimeIDCursor{}, ErrInvalidCursor
	}
	return TimeIDCursor{Time: cursor.Time, ID: cursor.ID}, nil
}
