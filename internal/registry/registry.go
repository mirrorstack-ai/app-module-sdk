// Package registry collects per-module declarations (routes, events, schedules)
// for the manifest endpoint to expose to the platform on deploy.
//
// Routes are recorded by Module.Platform/Public/Internal as the developer
// registers handlers. Events and schedules are placeholders for issue #9
// (ms.OnEvent / ms.Emit / ms.Cron) — the registry exposes empty defaults so
// the manifest payload shape is stable.
package registry

import (
	"maps"
	"slices"
	"sync"
)

// Scope identifies which auth boundary a route belongs to. The three values
// match the three Module.Platform/Public/Internal entry points.
type Scope string

const (
	ScopePlatform Scope = "platform"
	ScopePublic   Scope = "public"
	ScopeInternal Scope = "internal"
)

// AllScopes returns the canonical ordering of scopes. The manifest endpoint
// iterates this so every scope appears in the payload even when no routes
// are registered for it.
func AllScopes() []Scope {
	return []Scope{ScopePlatform, ScopePublic, ScopeInternal}
}

// IsValid reports whether s is one of the three known scopes.
// AddRoute panics on unknown scopes — the type accepts arbitrary strings,
// but only ScopePlatform/ScopePublic/ScopeInternal are valid scope keys in
// the manifest payload, and only SDK-internal code should be calling AddRoute.
func (s Scope) IsValid() bool {
	return s == ScopePlatform || s == ScopePublic || s == ScopeInternal
}

type Route struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

type Schedule struct {
	Name string `json:"name"`
	Cron string `json:"cron"`
}

// Registry is the per-module registry of routes/events/schedules.
// All operations are safe for concurrent use.
type Registry struct {
	mu         sync.RWMutex
	routes     map[Scope][]Route
	emits      []string
	subscribes map[string]string // event name → internal path
	schedules  []Schedule
}

func New() *Registry {
	return &Registry{
		routes:     make(map[Scope][]Route),
		subscribes: make(map[string]string),
	}
}

// AddRoute records a route under the given scope. First-wins: duplicate
// (scope, method, path) triples are dropped. Panics on an unknown scope —
// only SDK-internal code calls this, so an unknown value is a programmer error.
func (r *Registry) AddRoute(scope Scope, method, path string) {
	if !scope.IsValid() {
		panic("mirrorstack/registry: unknown scope " + string(scope))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.routes[scope] {
		if existing.Method == method && existing.Path == path {
			return
		}
	}
	r.routes[scope] = append(r.routes[scope], Route{Method: method, Path: path})
}

// Routes returns a copy of all routes grouped by scope. Every scope in
// AllScopes() is present in the result with at least an empty slice, so
// callers can rely on the field shape without nil checks.
func (r *Registry) Routes() map[Scope][]Route {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[Scope][]Route, len(AllScopes()))
	for _, scope := range AllScopes() {
		out[scope] = slices.Clone(r.routes[scope])
		if out[scope] == nil {
			out[scope] = []Route{}
		}
	}
	return out
}

// AddEmit declares that the module emits an event of the given name.
// First-wins: duplicate names are dropped.
func (r *Registry) AddEmit(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if slices.Contains(r.emits, name) {
		return
	}
	r.emits = append(r.emits, name)
}

// Emits returns a non-nil copy of all declared emit events.
func (r *Registry) Emits() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.emits == nil {
		return []string{}
	}
	return slices.Clone(r.emits)
}

// AddSubscribe declares that the module subscribes to an event from another
// module. The handler is mounted at path on the Internal scope. First-wins:
// a second AddSubscribe for the same event name is dropped.
func (r *Registry) AddSubscribe(name, path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.subscribes[name]; exists {
		return
	}
	r.subscribes[name] = path
}

// Subscribes returns a non-nil copy of all event subscriptions.
func (r *Registry) Subscribes() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.subscribes == nil {
		return map[string]string{}
	}
	return maps.Clone(r.subscribes)
}

// AddSchedule registers a cron job. First-wins by name: a second
// AddSchedule with the same name is dropped.
func (r *Registry) AddSchedule(name, cron string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.schedules {
		if existing.Name == name {
			return
		}
	}
	r.schedules = append(r.schedules, Schedule{Name: name, Cron: cron})
}

// Schedules returns a non-nil copy of all scheduled jobs.
func (r *Registry) Schedules() []Schedule {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.schedules == nil {
		return []Schedule{}
	}
	return slices.Clone(r.schedules)
}
