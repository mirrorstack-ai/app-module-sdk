// Package contributions implements the SDK's host-side contribution
// slots: a host module declares an extension point with
// ms.DefineContribute, the SDK auto-mounts HTTP endpoints for
// register/unregister/list, and other modules register payloads
// against the slot via the Contribute call (or direct HTTP).
//
// Storage shape (per host module):
//
//	<modulePrefix>_contributions (
//	    slot            text NOT NULL,
//	    contribution_id text NOT NULL,
//	    payload         jsonb NOT NULL,
//	    registered_at   timestamptz NOT NULL DEFAULT now(),
//	    PRIMARY KEY (slot, contribution_id)
//	)
//
// The host names the table prefix once (Config.ID) and the SDK creates
// the table on Start if any slot has been declared. Production schema
// management is handled by the lifecycle install hook in a follow-up.
package contributions

import (
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"
)

var (
	ErrSlotNotDefined = errors.New("contributions: slot not defined")
	ErrInvalidPayload = errors.New("contributions: invalid payload")
	ErrEmptyKey       = errors.New("contributions: slot key is empty")
	ErrEmptyID        = errors.New("contributions: contribution id is empty")
)

// Slot is one declared contribution point. The validator closure is
// produced by DefineContribute generic so the SDK can typecheck
// incoming payloads against the host's declared T at register time —
// without the storage layer needing to know what T is.
type Slot struct {
	Key       string
	validate  func(data json.RawMessage) error
	schemaTag string
}

// Validate runs the per-slot JSON validator against an incoming
// payload. Wraps ErrInvalidPayload so handlers can sniff for a 400
// without leaking internal type info to the caller.
func (s Slot) Validate(payload json.RawMessage) error {
	if s.validate == nil {
		return nil
	}
	if err := s.validate(payload); err != nil {
		return errors.Join(ErrInvalidPayload, err)
	}
	return nil
}

// SchemaTag returns the Go-type-name hint surfaced on the manifest.
func (s Slot) SchemaTag() string { return s.schemaTag }

// Contribution is a stored row, returned by the list endpoint.
type Contribution struct {
	ID           string          `json:"id"`
	Payload      json.RawMessage `json:"payload"`
	RegisteredAt time.Time       `json:"registered_at"`
}

// Registry holds all declared slots for one module. Populated at
// DefineContribute time; consulted by the HTTP handlers + auto-mount
// path to figure out which routes to expose.
type Registry struct {
	mu    sync.RWMutex
	slots map[string]Slot
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{slots: make(map[string]Slot)}
}

// Define stores the slot. Re-defining the same key panics — slot
// declarations are static at module init.
func (r *Registry) Define(s Slot) error {
	if s.Key == "" {
		return ErrEmptyKey
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.slots[s.Key]; dup {
		return errors.New("contributions: slot already defined: " + s.Key)
	}
	r.slots[s.Key] = s
	return nil
}

// Get looks up a slot by key. Returns (Slot{}, false) if undefined.
func (r *Registry) Get(key string) (Slot, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.slots[key]
	return s, ok
}

// Len returns the number of declared slots. Lets callers decide
// "is this module accepting any contributions?" without paying the
// allocation cost of List() — used in core.Module.Start to skip the
// EnsureTable round-trip when no slots are declared.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.slots)
}

// SlotInfo is the manifest-shaped projection of a Slot — no closure,
// just metadata the manifest serializes.
type SlotInfo struct {
	Key       string `json:"key"`
	SchemaTag string `json:"schemaTag,omitempty"`
}

// List returns every declared slot's manifest projection, sorted by
// key for stable output. Single RLock for the whole walk so no
// concurrent Define can change the map between key collection and
// row read.
func (r *Registry) List() []SlotInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]SlotInfo, 0, len(r.slots))
	for _, s := range r.slots {
		out = append(out, SlotInfo{Key: s.Key, SchemaTag: s.schemaTag})
	}
	slices.SortFunc(out, func(a, b SlotInfo) int {
		if a.Key < b.Key {
			return -1
		}
		if a.Key > b.Key {
			return 1
		}
		return 0
	})
	return out
}

// NewSlot constructs a Slot with a validator closure. Generic
// DefineContribute[T] in the parent package builds the closure
// where T is in scope.
func NewSlot(key string, schemaTag string, validate func(json.RawMessage) error) Slot {
	return Slot{Key: key, schemaTag: schemaTag, validate: validate}
}
