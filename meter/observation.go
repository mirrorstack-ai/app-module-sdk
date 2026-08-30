package meter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	observationEnvelopeVersion = 2

	maxSubjectBytes        = 256
	maxMetadataBytes       = 4 << 10
	maxMetadataMembers     = 32
	maxMetadataDepth       = 4
	maxMetadataKeyBytes    = 64
	maxMetadataStringBytes = 512
	maxMetadataArrayItems  = 32
	maxMetadataNumberBytes = 128
)

// Observation carries the caller-owned identity and occurrence evidence for a
// v2 meter event. EventID must be persisted with the billable state change and
// reused across ambiguous retries. Subject is an opaque end-user identity;
// Metadata is bounded diagnostic JSON and never an aggregation key. OccurredAt
// is the original event time, not the delivery/receipt time. The SDK requires
// it and normalizes it to UTC, but does not compare it with the module process
// clock. API and Billing own occurrence-window, replay, and billing-period
// decisions against authoritative server state.
type Observation struct {
	EventID    string
	Subject    string
	Metadata   json.RawMessage
	OccurredAt time.Time
}

// observationEvent is private so adding the v2 fields does not change the
// exported v1 Event struct's source compatibility. Its field order pins the
// canonical SDK-to-Dispatch JSON body. RecordedAtHint exists for envelope
// continuity only: Dispatch discards it and stamps Billing's authoritative
// recorded_at from the server clock.
type observationEvent struct {
	V              int             `json:"v"`
	EventID        string          `json:"eventId"`
	AppIDHint      string          `json:"appIdHint,omitempty"`
	ModuleIDHint   string          `json:"moduleIdHint"`
	Metric         string          `json:"metric"`
	Value          float64         `json:"value"`
	Subject        string          `json:"subject,omitempty"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
	OccurredAt     time.Time       `json:"occurredAt"`
	RecordedAtHint time.Time       `json:"recordedAtHint"`
}

func validateObservation(observation Observation, subjectRequired bool) (Observation, error) {
	if err := validateEventID(observation.EventID); err != nil {
		return Observation{}, err
	}
	if observation.OccurredAt.IsZero() {
		return Observation{}, errors.New("mirrorstack/meter: observation occurredAt is required")
	}
	if err := validateSubject(observation.Subject); err != nil {
		return Observation{}, fmt.Errorf("mirrorstack/meter: observation subject: %w", err)
	}
	if subjectRequired && observation.Subject == "" {
		return Observation{}, errors.New("mirrorstack/meter: observation subject is required for a subject-keyed metric")
	}
	metadata, err := validateAndCopyMetadata(observation.Metadata)
	if err != nil {
		return Observation{}, fmt.Errorf("mirrorstack/meter: observation metadata: %w", err)
	}

	observation.Metadata = metadata
	observation.OccurredAt = observation.OccurredAt.UTC()
	return observation, nil
}

func validateSubject(subject string) error {
	if subject == "" {
		return nil
	}
	if len(subject) > maxSubjectBytes {
		return fmt.Errorf("subject exceeds %d bytes", maxSubjectBytes)
	}
	if !utf8.ValidString(subject) {
		return errors.New("subject must be valid UTF-8")
	}
	for _, r := range subject {
		if unicode.IsControl(r) {
			return errors.New("subject must not contain control characters")
		}
	}
	return nil
}

// validateAndCopyMetadata mirrors Billing's v2 admission bounds while keeping
// the caller's JSON value intact for Dispatch. Billing remains authoritative
// and canonicalizes the object for its idempotency fingerprint; the SDK only
// rejects inputs that cannot possibly be accepted and does not decode/re-encode
// its numbers or key order. Envelope encoding may remove insignificant space.
func validateAndCopyMetadata(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > maxMetadataBytes {
		return nil, fmt.Errorf("metadata exceeds %d bytes", maxMetadataBytes)
	}
	if !utf8.Valid(raw) {
		return nil, errors.New("metadata must be valid UTF-8 JSON")
	}
	if err := validateMetadataMemberNames(raw); err != nil {
		return nil, err
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("metadata must be valid JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("metadata must contain exactly one JSON value")
		}
		return nil, fmt.Errorf("metadata has trailing data: %w", err)
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, errors.New("metadata must be a JSON object")
	}

	state := metadataValidationState{}
	var canonical bytes.Buffer
	if err := appendCanonicalMetadata(&canonical, value, 1, &state); err != nil {
		return nil, err
	}
	if canonical.Len() > maxMetadataBytes {
		return nil, fmt.Errorf("canonical metadata exceeds %d bytes", maxMetadataBytes)
	}
	return append(json.RawMessage(nil), raw...), nil
}

// validateMetadataMemberNames walks the source token stream before decoding
// into maps. encoding/json otherwise collapses duplicate names, which would
// make validation depend on last-write-wins parser behavior.
func validateMetadataMemberNames(raw json.RawMessage) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	members := 0
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				nameToken, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := nameToken.(string)
				if !ok {
					return errors.New("metadata object name must be a string")
				}
				members++
				if members > maxMetadataMembers {
					return fmt.Errorf("metadata exceeds %d object members", maxMetadataMembers)
				}
				if _, duplicate := seen[name]; duplicate {
					return fmt.Errorf("metadata contains duplicate key %q", name)
				}
				seen[name] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("metadata contains an unexpected JSON delimiter")
		}
	}
	return walk()
}

type metadataValidationState struct {
	members int
}

func appendCanonicalMetadata(dst *bytes.Buffer, value any, depth int, state *metadataValidationState) error {
	if depth > maxMetadataDepth {
		return fmt.Errorf("metadata exceeds maximum depth %d", maxMetadataDepth)
	}
	switch value := value.(type) {
	case map[string]any:
		state.members += len(value)
		if state.members > maxMetadataMembers {
			return fmt.Errorf("metadata exceeds %d object members", maxMetadataMembers)
		}
		keys := make([]string, 0, len(value))
		for key := range value {
			if err := validateMetadataKey(key); err != nil {
				return err
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		dst.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				dst.WriteByte(',')
			}
			encodedKey, _ := json.Marshal(key)
			dst.Write(encodedKey)
			dst.WriteByte(':')
			if err := appendCanonicalMetadata(dst, value[key], depth+1, state); err != nil {
				return err
			}
		}
		dst.WriteByte('}')
	case []any:
		if len(value) > maxMetadataArrayItems {
			return fmt.Errorf("metadata array exceeds %d items", maxMetadataArrayItems)
		}
		dst.WriteByte('[')
		for i, item := range value {
			if i > 0 {
				dst.WriteByte(',')
			}
			if err := appendCanonicalMetadata(dst, item, depth+1, state); err != nil {
				return err
			}
		}
		dst.WriteByte(']')
	case string:
		if len(value) > maxMetadataStringBytes {
			return fmt.Errorf("metadata string exceeds %d bytes", maxMetadataStringBytes)
		}
		encoded, _ := json.Marshal(value)
		dst.Write(encoded)
	case json.Number:
		if len(value.String()) > maxMetadataNumberBytes {
			return fmt.Errorf("metadata number exceeds %d bytes", maxMetadataNumberBytes)
		}
		number, err := strconv.ParseFloat(value.String(), 64)
		if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
			return errors.New("metadata number must be finite float64")
		}
		canonical, err := canonicalJSONNumber(value.String())
		if err != nil {
			return err
		}
		dst.WriteString(canonical)
	case bool:
		if value {
			dst.WriteString("true")
		} else {
			dst.WriteString("false")
		}
	case nil:
		dst.WriteString("null")
	default:
		return fmt.Errorf("metadata contains unsupported value type %T", value)
	}
	return nil
}

// canonicalJSONNumber matches Billing's exact-decimal normalization. The SDK
// uses the bytes only to enforce Billing's canonical 4 KiB limit; it still
// sends the original RawMessage.
func canonicalJSONNumber(source string) (string, error) {
	negative := false
	if strings.HasPrefix(source, "-") {
		negative = true
		source = source[1:]
	}

	mantissa, exponentSource, hasExponent := source, "", false
	if index := strings.IndexAny(source, "eE"); index >= 0 {
		mantissa, exponentSource, hasExponent = source[:index], source[index+1:], true
	}
	exponent := int64(0)
	if hasExponent {
		parsed, err := strconv.ParseInt(exponentSource, 10, 64)
		if err != nil {
			return "", errors.New("metadata number exponent is out of range")
		}
		exponent = parsed
	}

	integer, fraction := mantissa, ""
	if index := strings.IndexByte(mantissa, '.'); index >= 0 {
		integer, fraction = mantissa[:index], mantissa[index+1:]
	}
	if exponent < math.MinInt64+int64(len(fraction)) {
		return "", errors.New("metadata number exponent is out of range")
	}
	exponent -= int64(len(fraction))
	digits := strings.TrimLeft(integer+fraction, "0")
	if digits == "" {
		return "0", nil
	}
	trimmed := strings.TrimRight(digits, "0")
	trailingZeroes := len(digits) - len(trimmed)
	if exponent > math.MaxInt64-int64(trailingZeroes) {
		return "", errors.New("metadata number exponent is out of range")
	}
	exponent += int64(trailingZeroes)

	var canonical strings.Builder
	if negative {
		canonical.WriteByte('-')
	}
	canonical.WriteString(trimmed)
	if exponent != 0 {
		canonical.WriteByte('e')
		canonical.WriteString(strconv.FormatInt(exponent, 10))
	}
	return canonical.String(), nil
}

func validateMetadataKey(key string) error {
	if len(key) == 0 || len(key) > maxMetadataKeyBytes {
		return fmt.Errorf("metadata key must be between 1 and %d bytes", maxMetadataKeyBytes)
	}
	if !isASCIILetter(key[0]) {
		return fmt.Errorf("metadata key %q must start with an ASCII letter", key)
	}
	for i := 1; i < len(key); i++ {
		c := key[i]
		if !isASCIILetter(c) && (c < '0' || c > '9') && c != '_' && c != '.' && c != '-' {
			return fmt.Errorf("metadata key %q contains an invalid character", key)
		}
	}
	return nil
}

func isASCIILetter(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}
