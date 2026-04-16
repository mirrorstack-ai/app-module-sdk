// Package registry collects per-module declarations (routes, events,
// schedules, permissions) for the manifest endpoint to expose to the platform
// on deploy.
//
// Routes are recorded by Module.Platform/Public/Internal as the developer
// registers handlers. Permissions are recorded by Module.RequirePermission.
// Events and schedules are recorded by Module.OnEvent / Module.Emits /
// Module.Cron (issue #9). The registry exposes empty defaults so the
// manifest payload shape is stable even when nothing is registered.
package registry

import (
	"context"
	"encoding/json"
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

// Schedule is a cron job declaration. Path is the URL the platform's
// scheduler invokes (POSTs to) when the cron fires; the SDK auto-derives
// it as /__mirrorstack/crons/{name} on the module's Internal scope.
type Schedule struct {
	Name string `json:"name"`
	Cron string `json:"cron"`
	Path string `json:"path"`
}

// Task is a declared background task. Exposed in the manifest so the platform
// can provision SQS queues and ECS task definitions on deploy.
type Task struct {
	Name        string `json:"name"`
	MaxDuration string `json:"maxDuration,omitempty"` // e.g. "600s", "10m" — platform sets visibility timeout
	MaxRetries  int    `json:"maxRetries,omitempty"`  // platform configures DLQ redrive policy
}

// Permission is a declared module permission. Exposed in the manifest so the
// platform can surface "what does this module need" on its install screen.
type Permission struct {
	Name  string   `json:"name"`
	Roles []string `json:"roles"`
}

// Dependency is a declared dependency on another module. Optional=true means
// the dependent module works standalone and uses the dependency opportunistically
// at runtime via Resolve[T]; Optional=false means the platform must install the
// dependency before this module.
//
// Version is a SemVer constraint string (npm-style: "^1.2.0", "~1.2.0",
// ">=1.2.0 <2.0.0", "1.x", exact "1.2.3", or "" for any). The platform
// catalog enforces matching at install time.
type Dependency struct {
	ID       string `json:"id"`
	Version  string `json:"version,omitempty"`
	Optional bool   `json:"optional,omitempty"`
}

// MCPToolHandler is the type-erased handler signature used after generic
// MCPTool registration at the SDK level has wrapped the typed handler.
type MCPToolHandler func(ctx context.Context, args json.RawMessage) (json.RawMessage, error)

// MCPResourceHandler returns the current content of an MCP resource on demand.
type MCPResourceHandler func(ctx context.Context) (json.RawMessage, error)

// MCPToolDecl is a registered MCP tool. Name, Description, and schemas appear
// in the manifest (via MCPToolExport, omitting Handler). Handler is invoked
// by the /__mirrorstack/mcp/tools/call route.
type MCPToolDecl struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	Handler      MCPToolHandler  `json:"-"`
}

// MCPResourceDecl is a registered MCP resource.
type MCPResourceDecl struct {
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Schema      json.RawMessage    `json:"schema,omitempty"`
	Handler     MCPResourceHandler `json:"-"`
}

// Registry is the per-module registry of routes/events/schedules/tasks/permissions.
// All operations are safe for concurrent use.
type Registry struct {
	mu           sync.RWMutex
	routes       map[Scope][]Route
	emits        []string
	subscribes   map[string]string // event name → internal path
	schedules    []Schedule
	tasks        []Task
	permissions  []Permission
	description  string
	dependencies []Dependency
	mcpTools     []MCPToolDecl
	mcpResources []MCPResourceDecl
}

func New() *Registry {
	return &Registry{
		routes:     make(map[Scope][]Route),
		subscribes: make(map[string]string),
	}
}

// SetDescription sets the module's human-readable description. Last-write-wins;
// typically called once at module init via ms.Describe.
func (r *Registry) SetDescription(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.description = s
}

// Description returns the module description, or empty string if unset.
func (r *Registry) Description() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.description
}

// AddDependency records a dependency on another module. The Optional flag
// distinguishes required (install-time) from optional (runtime Resolve) deps.
// Version carries a SemVer constraint string (already validated by the
// caller); empty means any version is acceptable.
//
// Dedup: if the same ID is declared both ways across the codebase, required
// wins (stricter beats looser). When a later required declaration upgrades a
// prior optional entry, the later declaration's Version replaces the earlier
// one (the required caller's constraint is authoritative). For same-type
// redeclarations (both required or both optional), first-wins — including
// for the Version field.
//
// Returns true if the dependency was newly added or upgraded from optional
// to required, false if the call was a no-op.
func (r *Registry) AddDependency(dep Dependency) bool {
	ValidateName("DependsOn", dep.ID)
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, existing := range r.dependencies {
		if existing.ID == dep.ID {
			if existing.Optional && !dep.Optional {
				r.dependencies[i].Optional = false
				r.dependencies[i].Version = dep.Version
				return true
			}
			return false
		}
	}
	r.dependencies = append(r.dependencies, dep)
	return true
}

// Dependencies returns a non-nil copy of all declared dependencies in
// registration order.
func (r *Registry) Dependencies() []Dependency {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.dependencies == nil {
		return []Dependency{}
	}
	return slices.Clone(r.dependencies)
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
// Returns true if added, false if a declaration for that name already exists
// (first-wins). Panics on an invalid name (see ValidateName).
func (r *Registry) AddEmit(name string) bool {
	ValidateName("Emits", name)
	r.mu.Lock()
	defer r.mu.Unlock()
	if slices.Contains(r.emits, name) {
		return false
	}
	r.emits = append(r.emits, name)
	return true
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
// module. The handler is mounted at path on the Internal scope. Returns true
// if added, false if a subscription for that event name already exists
// (first-wins). Panics on an invalid name (see ValidateName).
func (r *Registry) AddSubscribe(name, path string) bool {
	ValidateName("OnEvent", name)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.subscribes[name]; exists {
		return false
	}
	r.subscribes[name] = path
	return true
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

// AddSchedule registers a cron job. Returns true if added, false if a job
// with the same name already exists (first-wins). Panics on an invalid name
// (see ValidateName).
func (r *Registry) AddSchedule(name, cron, path string) bool {
	ValidateName("Cron", name)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.schedules {
		if existing.Name == name {
			return false
		}
	}
	r.schedules = append(r.schedules, Schedule{Name: name, Cron: cron, Path: path})
	return true
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

// AddTask declares a background task. Returns true if added, false if a task
// with the same name already exists (first-wins). Panics on an invalid name
// (see ValidateName).
func (r *Registry) AddTask(task Task) bool {
	ValidateName("OnTask", task.Name)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.tasks {
		if existing.Name == task.Name {
			return false
		}
	}
	r.tasks = append(r.tasks, task)
	return true
}

// Tasks returns a non-nil copy of all declared tasks.
func (r *Registry) Tasks() []Task {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.tasks == nil {
		return []Task{}
	}
	return slices.Clone(r.tasks)
}

// AddPermission records a declared permission. First-wins by name: a second
// AddPermission for the same name is dropped (matches AddRoute / AddEmit /
// AddSchedule semantics). The roles slice is cloned so caller mutations
// after the call cannot leak into the stored copy. Panics on an invalid
// name (see ValidateName) — permissions don't end up in URL
// paths, so the path-separator check is purely cosmetic for permissions,
// but the consistency with AddSubscribe/AddEmit/AddSchedule prevents
// downstream consumers (DB columns, log parsers) from receiving malformed
// strings via the manifest.
func (r *Registry) AddPermission(name string, roles []string) {
	ValidateName("RequirePermission", name)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.permissions {
		if existing.Name == name {
			return
		}
	}
	r.permissions = append(r.permissions, Permission{
		Name:  name,
		Roles: slices.Clone(roles),
	})
}

// Permissions returns a non-nil deep copy of all declared permissions. The
// roles slice on each entry is cloned so caller mutations cannot leak back.
//
// The hand-rolled loop is required: slices.Clone is shallow, and a shallow
// clone of []Permission would share each entry's Roles slice with the
// registry. TestPermissions_RolesAreCloned is the regression guard.
func (r *Registry) Permissions() []Permission {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.permissions == nil {
		return []Permission{}
	}
	out := make([]Permission, len(r.permissions))
	for i, p := range r.permissions {
		out[i] = Permission{Name: p.Name, Roles: slices.Clone(p.Roles)}
	}
	return out
}

// AddMCPTool registers an MCP tool. First-wins: if a tool with the same name
// already exists, the call is a no-op and returns false. Panics on an invalid
// name (see ValidateName).
func (r *Registry) AddMCPTool(tool MCPToolDecl) bool {
	ValidateName("MCPTool", tool.Name)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.mcpTools {
		if existing.Name == tool.Name {
			return false
		}
	}
	r.mcpTools = append(r.mcpTools, tool)
	return true
}

// AddMCPResource registers an MCP resource. Same first-wins rule as AddMCPTool.
func (r *Registry) AddMCPResource(rc MCPResourceDecl) bool {
	ValidateName("MCPResource", rc.Name)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.mcpResources {
		if existing.Name == rc.Name {
			return false
		}
	}
	r.mcpResources = append(r.mcpResources, rc)
	return true
}

// MCPTool returns the registered tool with the given name, or the zero value
// and false if no such tool is registered.
func (r *Registry) MCPTool(name string) (MCPToolDecl, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, t := range r.mcpTools {
		if t.Name == name {
			return t, true
		}
	}
	return MCPToolDecl{}, false
}

// MCPResource returns the registered resource with the given name, or the zero
// value and false if no such resource is registered.
func (r *Registry) MCPResource(name string) (MCPResourceDecl, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, rc := range r.mcpResources {
		if rc.Name == name {
			return rc, true
		}
	}
	return MCPResourceDecl{}, false
}

// MCPTools returns a non-nil copy of all registered tools in registration order.
// Handlers are preserved in the copy; the slice header is independent.
func (r *Registry) MCPTools() []MCPToolDecl {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.mcpTools == nil {
		return []MCPToolDecl{}
	}
	return slices.Clone(r.mcpTools)
}

// MCPResources returns a non-nil copy of all registered resources in
// registration order.
func (r *Registry) MCPResources() []MCPResourceDecl {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.mcpResources == nil {
		return []MCPResourceDecl{}
	}
	return slices.Clone(r.mcpResources)
}
