package registry

import "testing"

func TestAddRoute_GroupsByScope(t *testing.T) {
	t.Parallel()

	r := New()
	r.AddRoute(ScopePlatform, "GET", "/items")
	r.AddRoute(ScopePlatform, "POST", "/items")
	r.AddRoute(ScopePublic, "GET", "/items")
	r.AddRoute(ScopeInternal, "POST", "/events/foo")

	got := r.Routes()
	if len(got[ScopePlatform]) != 2 {
		t.Errorf("platform routes = %d, want 2", len(got[ScopePlatform]))
	}
	if len(got[ScopePublic]) != 1 {
		t.Errorf("public routes = %d, want 1", len(got[ScopePublic]))
	}
	if len(got[ScopeInternal]) != 1 {
		t.Errorf("internal routes = %d, want 1", len(got[ScopeInternal]))
	}
}

func TestAddRoute_DropsDuplicates(t *testing.T) {
	t.Parallel()

	r := New()
	r.AddRoute(ScopePlatform, "GET", "/items")
	r.AddRoute(ScopePlatform, "GET", "/items")
	r.AddRoute(ScopePlatform, "GET", "/items")

	got := r.Routes()
	if len(got[ScopePlatform]) != 1 {
		t.Errorf("expected 1 route after 3 identical adds, got %d", len(got[ScopePlatform]))
	}
}

func TestAddRoute_PanicsOnUnknownScope(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for unknown scope")
		}
	}()
	r := New()
	r.AddRoute(Scope("../etc/passwd"), "GET", "/x")
}

func TestScope_IsValid(t *testing.T) {
	t.Parallel()

	for _, valid := range []Scope{ScopePlatform, ScopePublic, ScopeInternal} {
		if !valid.IsValid() {
			t.Errorf("scope %q should be valid", valid)
		}
	}
	for _, invalid := range []Scope{"", "Platform", "admin", "../etc"} {
		if invalid.IsValid() {
			t.Errorf("scope %q should NOT be valid", invalid)
		}
	}
}

func TestRoutes_AlwaysHasAllScopes(t *testing.T) {
	t.Parallel()

	// Empty registry must still return all three scopes as empty slices, so
	// the manifest payload shape is stable.
	r := New()
	got := r.Routes()
	for _, scope := range AllScopes() {
		v, ok := got[scope]
		if !ok {
			t.Errorf("scope %q missing from empty Routes()", scope)
		}
		if v == nil {
			t.Errorf("scope %q is nil, want empty slice", scope)
		}
	}
}

func TestRoutes_ReturnsCopy(t *testing.T) {
	t.Parallel()

	r := New()
	r.AddRoute(ScopePlatform, "GET", "/items")

	first := r.Routes()
	first[ScopePlatform] = append(first[ScopePlatform], Route{Method: "POST", Path: "/injected"})

	second := r.Routes()
	if len(second[ScopePlatform]) != 1 {
		t.Errorf("Routes() returned a shared slice: caller mutation leaked back")
	}
}

func TestEmits_DropsDuplicates(t *testing.T) {
	t.Parallel()

	r := New()
	r.AddEmit("user.created")
	r.AddEmit("user.created")
	r.AddEmit("user.deleted")

	got := r.Emits()
	if len(got) != 2 {
		t.Errorf("emits = %v, want 2 distinct events", got)
	}
}

func TestEmits_EmptyReturnsNonNil(t *testing.T) {
	t.Parallel()
	if got := New().Emits(); got == nil {
		t.Error("empty Emits() returned nil, want []string{}")
	}
}

func TestSubscribes_KeyedByEventName(t *testing.T) {
	t.Parallel()

	r := New()
	r.AddSubscribe("oauth.user_deleted", "/internal/events/on-user-deleted")
	r.AddSubscribe("billing.payment_succeeded", "/internal/events/on-payment")

	got := r.Subscribes()
	if got["oauth.user_deleted"] != "/internal/events/on-user-deleted" {
		t.Errorf("oauth.user_deleted path = %q", got["oauth.user_deleted"])
	}
	if got["billing.payment_succeeded"] != "/internal/events/on-payment" {
		t.Errorf("billing path = %q", got["billing.payment_succeeded"])
	}
}

func TestSubscribes_FirstWins(t *testing.T) {
	t.Parallel()

	// Match AddRoute / AddEmit semantics: a second AddSubscribe for the same
	// event name must NOT silently overwrite the first.
	r := New()
	r.AddSubscribe("user.created", "/internal/events/handler-a")
	r.AddSubscribe("user.created", "/internal/events/handler-b")

	got := r.Subscribes()
	if got["user.created"] != "/internal/events/handler-a" {
		t.Errorf("user.created = %q, want first-wins handler-a", got["user.created"])
	}
}

func TestSubscribes_ReturnsCopy(t *testing.T) {
	t.Parallel()

	r := New()
	r.AddSubscribe("a", "/path-a")

	first := r.Subscribes()
	first["b"] = "/injected"

	second := r.Subscribes()
	if _, ok := second["b"]; ok {
		t.Error("Subscribes() returned a shared map: caller mutation leaked back")
	}
}

func TestSubscribes_EmptyReturnsNonNil(t *testing.T) {
	t.Parallel()
	if got := New().Subscribes(); got == nil {
		t.Error("empty Subscribes() returned nil, want map{}")
	}
}

func TestSchedules_Recorded(t *testing.T) {
	t.Parallel()

	r := New()
	r.AddSchedule("cleanup-temp", "0 3 * * *", "/crons/cleanup-temp")
	r.AddSchedule("daily-report", "0 9 * * *", "/crons/daily-report")

	got := r.Schedules()
	if len(got) != 2 {
		t.Fatalf("schedules = %v, want 2", got)
	}
	if got[0].Name != "cleanup-temp" || got[0].Cron != "0 3 * * *" || got[0].Path != "/crons/cleanup-temp" {
		t.Errorf("schedules[0] = %+v, want {cleanup-temp, 0 3 * * *, /crons/cleanup-temp}", got[0])
	}
}

func TestSchedules_DropsDuplicateNames(t *testing.T) {
	t.Parallel()

	// Two schedules with the same name (even with different crons) is a
	// configuration mistake — first-wins matches AddRoute / AddEmit.
	r := New()
	r.AddSchedule("cleanup", "0 3 * * *", "/crons/cleanup")
	r.AddSchedule("cleanup", "0 5 * * *", "/crons/cleanup-other")

	got := r.Schedules()
	if len(got) != 1 {
		t.Fatalf("schedules = %d, want 1 (first-wins by name)", len(got))
	}
	if got[0].Cron != "0 3 * * *" {
		t.Errorf("schedules[0].Cron = %q, want 0 3 * * * (first wins)", got[0].Cron)
	}
}

func TestSchedules_EmptyReturnsNonNil(t *testing.T) {
	t.Parallel()
	if got := New().Schedules(); got == nil {
		t.Error("empty Schedules() returned nil, want []Schedule{}")
	}
}

func TestAddX_ReturnBoolForFirstWins(t *testing.T) {
	t.Parallel()

	// Add* methods return true on first registration, false on duplicate.
	// This is the contract Module.OnEvent / Cron / Emits rely on to panic
	// on duplicates without a separate Has*-then-Add two-step.
	r := New()
	if !r.AddSubscribe("user.created", "/events/user.created") {
		t.Error("first AddSubscribe should return true")
	}
	if r.AddSubscribe("user.created", "/events/user.created") {
		t.Error("duplicate AddSubscribe should return false")
	}

	if !r.AddEmit("media.uploaded") {
		t.Error("first AddEmit should return true")
	}
	if r.AddEmit("media.uploaded") {
		t.Error("duplicate AddEmit should return false")
	}

	if !r.AddSchedule("nightly", "0 3 * * *", "/crons/nightly") {
		t.Error("first AddSchedule should return true")
	}
	if r.AddSchedule("nightly", "0 5 * * *", "/crons/nightly-other") {
		t.Error("duplicate AddSchedule should return false")
	}
}

func TestValidateRegistrationName_Rejects(t *testing.T) {
	t.Parallel()

	// SECURITY regression guard for the registry-level validation. The
	// rules apply uniformly to AddSubscribe / AddSchedule / AddEmit because
	// the validator is called from each Add*. Without this guard, names
	// like "../admin" would let chi normalize the registered pattern to
	// "/admin", silently escaping the /events/ or /crons/ namespace.
	cases := []struct {
		name string
		bad  string
	}{
		{"empty", ""},
		{"dot-segment", "../admin"},
		{"slash", "foo/bar"},
		{"backslash", "foo\\bar"},
		{"space", "foo bar"},
		{"tab", "foo\tbar"},
		{"newline", "foo\nbar"},
		{"carriage-return", "foo\rbar"},
		{"null-byte", "foo\x00bar"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := New()
			defer func() {
				if rec := recover(); rec == nil {
					t.Errorf("expected panic for AddSubscribe(%q)", tc.bad)
				}
			}()
			r.AddSubscribe(tc.bad, "/events/x")
		})
	}
}

func TestValidateRegistrationName_AcceptsValidNames(t *testing.T) {
	t.Parallel()

	// Reasonable names must NOT panic — the validator is a deny-list, not
	// an allow-list. If someone tightens it later this test catches the
	// over-rejection.
	good := []string{
		"created",
		"user.created",
		"oauth.user_deleted",
		"billing.payment_succeeded",
		"media-uploaded",
		"v1.user.created", // versioned event names should still pass
	}
	for _, name := range good {
		t.Run(name, func(t *testing.T) {
			r := New()
			defer func() {
				if rec := recover(); rec != nil {
					t.Errorf("name %q unexpectedly rejected: %v", name, rec)
				}
			}()
			r.AddSubscribe(name, "/events/"+name)
		})
	}
}

func TestPermissions_Recorded(t *testing.T) {
	t.Parallel()

	r := New()
	r.AddPermission("media.view", []string{"admin", "member", "viewer"})
	r.AddPermission("media.upload", []string{"admin", "member"})
	r.AddPermission("media.delete", []string{"admin"})

	got := r.Permissions()
	if len(got) != 3 {
		t.Fatalf("permissions = %d, want 3", len(got))
	}
	want := map[string]int{"media.view": 3, "media.upload": 2, "media.delete": 1}
	for _, p := range got {
		if n, ok := want[p.Name]; !ok || len(p.Roles) != n {
			t.Errorf("permission %+v doesn't match expected role counts %v", p, want)
		}
	}
}

func TestPermissions_FirstWinsByName(t *testing.T) {
	t.Parallel()

	// A second AddPermission with the same name is a developer mistake and
	// must NOT silently overwrite — matches AddRoute / AddEmit / AddSchedule.
	r := New()
	r.AddPermission("media.upload", []string{"admin", "member"})
	r.AddPermission("media.upload", []string{"admin"}) // narrower — must be dropped

	got := r.Permissions()
	if len(got) != 1 {
		t.Fatalf("permissions = %d, want 1 (first-wins by name)", len(got))
	}
	if len(got[0].Roles) != 2 {
		t.Errorf("first AddPermission roles overwritten: %v, want [admin member]", got[0].Roles)
	}
}

func TestPermissions_FirstWinsBlocksPrivilegeEscalation(t *testing.T) {
	t.Parallel()

	// SECURITY regression guard: the dangerous direction of duplicate-name
	// re-registration is stricter-first, looser-second (privilege escalation).
	// First-wins must block this — a buggy or malicious second call cannot
	// replace the original tight ruleset with a wider one.
	r := New()
	r.AddPermission("media.delete", []string{"admin"})                     // strict first
	r.AddPermission("media.delete", []string{"admin", "member", "viewer"}) // looser second — must be dropped

	got := r.Permissions()
	if len(got) != 1 {
		t.Fatalf("permissions = %d, want 1 (first-wins by name)", len(got))
	}
	if len(got[0].Roles) != 1 || got[0].Roles[0] != "admin" {
		t.Errorf("looser second registration leaked: %v, want [admin]", got[0].Roles)
	}
}

func TestPermissions_EmptyReturnsNonNil(t *testing.T) {
	t.Parallel()
	if got := New().Permissions(); got == nil {
		t.Error("empty Permissions() returned nil, want []Permission{}")
	}
}

func TestPermissions_RolesAreCloned(t *testing.T) {
	t.Parallel()

	// Caller mutations to the input roles slice (or to the returned slice)
	// must not leak into the registry's stored copy.
	r := New()
	roles := []string{"admin", "member"}
	r.AddPermission("x", roles)
	roles[0] = "TAMPERED" // mutate the input AFTER the call

	got := r.Permissions()
	if got[0].Roles[0] != "admin" {
		t.Errorf("input mutation leaked into registry: roles[0] = %q", got[0].Roles[0])
	}

	got[0].Roles[1] = "TAMPERED-OUT" // mutate the returned copy
	again := r.Permissions()
	if again[0].Roles[1] != "member" {
		t.Errorf("output mutation leaked into registry: roles[1] = %q", again[0].Roles[1])
	}
}

func TestAddPermission_PanicsOnInvalidName(t *testing.T) {
	t.Parallel()

	// Permissions don't end up in URL paths, but they DO appear in the
	// manifest payload which platform-side consumers may use as identifiers
	// for grant UI, RBAC tables, log fields, etc. Sharing the registry's
	// ValidateName guard with AddSubscribe/AddEmit/AddSchedule
	// prevents inconsistent behavior across the four registration sites
	// and keeps malformed strings (null bytes, dot-segments) out of the
	// manifest regardless of which Add* the developer called.
	cases := []struct {
		name string
		bad  string
	}{
		{"empty", ""},
		{"dot-segment", "../admin"},
		{"slash", "foo/bar"},
		{"null-byte", "foo\x00bar"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := New()
			defer func() {
				if rec := recover(); rec == nil {
					t.Errorf("expected panic for AddPermission(%q)", tc.bad)
				}
			}()
			r.AddPermission(tc.bad, []string{"admin"})
		})
	}
}

// ---- Description ----

func TestDescription_SetAndGet(t *testing.T) {
	t.Parallel()

	r := New()
	if got := r.Description(); got != "" {
		t.Errorf("empty Registry Description = %q, want empty", got)
	}
	r.SetDescription("A demo module")
	if got := r.Description(); got != "A demo module" {
		t.Errorf("Description = %q, want %q", got, "A demo module")
	}
}

func TestDescription_LastWins(t *testing.T) {
	t.Parallel()

	r := New()
	r.SetDescription("first")
	r.SetDescription("second")
	if got := r.Description(); got != "second" {
		t.Errorf("Description = %q, want %q (last-wins)", got, "second")
	}
}

// ---- Dependencies ----

func TestDependencies_AddRequired(t *testing.T) {
	t.Parallel()

	r := New()
	if added := r.AddDependency("oauth-core", false); !added {
		t.Error("AddDependency(new, required) = false, want true")
	}
	got := r.Dependencies()
	if len(got) != 1 || got[0].ID != "oauth-core" || got[0].Optional {
		t.Errorf("Dependencies = %+v, want [{ID:oauth-core, Optional:false}]", got)
	}
}

func TestDependencies_AddOptional(t *testing.T) {
	t.Parallel()

	r := New()
	r.AddDependency("video", true)
	got := r.Dependencies()
	if len(got) != 1 || got[0].ID != "video" || !got[0].Optional {
		t.Errorf("Dependencies = %+v, want [{ID:video, Optional:true}]", got)
	}
}

func TestDependencies_RequiredUpgradesOptional(t *testing.T) {
	t.Parallel()

	r := New()
	r.AddDependency("oauth-core", true) // optional first
	if upgraded := r.AddDependency("oauth-core", false); !upgraded {
		t.Error("AddDependency(optional→required) = false, want true (upgrade)")
	}
	got := r.Dependencies()
	if len(got) != 1 || got[0].Optional {
		t.Errorf("after required override: Dependencies = %+v, want Optional=false", got)
	}
}

func TestDependencies_OptionalDoesNotDowngradeRequired(t *testing.T) {
	t.Parallel()

	r := New()
	r.AddDependency("oauth-core", false) // required first
	if added := r.AddDependency("oauth-core", true); added {
		t.Error("AddDependency(required→optional) = true, want false (no-op)")
	}
	got := r.Dependencies()
	if len(got) != 1 || got[0].Optional {
		t.Errorf("after optional override of required: Dependencies = %+v, want Optional=false", got)
	}
}

func TestDependencies_DuplicateRequiredIsNoOp(t *testing.T) {
	t.Parallel()

	r := New()
	r.AddDependency("a", false)
	if added := r.AddDependency("a", false); added {
		t.Error("AddDependency(same required twice) = true, want false (no-op)")
	}
	if got := r.Dependencies(); len(got) != 1 {
		t.Errorf("len(Dependencies) = %d after duplicate, want 1", len(got))
	}
}

func TestDependencies_EmptyReturnsNonNil(t *testing.T) {
	t.Parallel()

	r := New()
	got := r.Dependencies()
	if got == nil {
		t.Error("Dependencies() on empty Registry returned nil, want []Dependency{}")
	}
	if len(got) != 0 {
		t.Errorf("len(Dependencies()) = %d, want 0", len(got))
	}
}

func TestDependencies_PreservesRegistrationOrder(t *testing.T) {
	t.Parallel()

	r := New()
	r.AddDependency("third", false)
	r.AddDependency("first", false)
	r.AddDependency("second", true)
	got := r.Dependencies()
	if len(got) != 3 || got[0].ID != "third" || got[1].ID != "first" || got[2].ID != "second" {
		t.Errorf("Dependencies = %+v, want registration order third, first, second", got)
	}
}

func TestDependencies_ValidateNameRejectsBad(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		bad  string
	}{
		{"empty", ""},
		{"dot-segment", "../oauth-core"},
		{"slash", "foo/bar"},
		{"null-byte", "foo\x00bar"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := New()
			defer func() {
				if rec := recover(); rec == nil {
					t.Errorf("expected panic for AddDependency(%q)", tc.bad)
				}
			}()
			r.AddDependency(tc.bad, false)
		})
	}
}

// ---- MCP ----

func TestMCPTool_AddAndList(t *testing.T) {
	t.Parallel()

	r := New()
	if added := r.AddMCPTool(MCPToolDecl{Name: "greet", Description: "Say hi"}); !added {
		t.Error("AddMCPTool(new) = false, want true")
	}
	if got := r.MCPTools(); len(got) != 1 || got[0].Name != "greet" {
		t.Errorf("MCPTools = %+v, want [{greet, ...}]", got)
	}
}

func TestMCPTool_DuplicateNameIsNoOp(t *testing.T) {
	t.Parallel()

	r := New()
	r.AddMCPTool(MCPToolDecl{Name: "greet", Description: "first"})
	if added := r.AddMCPTool(MCPToolDecl{Name: "greet", Description: "second"}); added {
		t.Error("duplicate AddMCPTool = true, want false (first-wins)")
	}
	got := r.MCPTools()
	if len(got) != 1 || got[0].Description != "first" {
		t.Errorf("MCPTools[0].Description = %q, want first (first-wins)", got[0].Description)
	}
}

func TestMCPTool_LookupByName(t *testing.T) {
	t.Parallel()

	r := New()
	r.AddMCPTool(MCPToolDecl{Name: "a"})
	r.AddMCPTool(MCPToolDecl{Name: "b"})

	if _, ok := r.MCPTool("a"); !ok {
		t.Error("MCPTool(a) not found")
	}
	if _, ok := r.MCPTool("missing"); ok {
		t.Error("MCPTool(missing) returned ok=true")
	}
}

func TestMCPTool_EmptyReturnsNonNil(t *testing.T) {
	t.Parallel()

	r := New()
	if got := r.MCPTools(); got == nil {
		t.Error("MCPTools() on empty = nil, want []MCPToolDecl{}")
	}
	if got := r.MCPResources(); got == nil {
		t.Error("MCPResources() on empty = nil, want []MCPResourceDecl{}")
	}
}

func TestMCPTool_ValidateNameRejectsBad(t *testing.T) {
	t.Parallel()

	bad := []string{"", "../etc", "foo/bar", "foo\x00bar"}
	for _, b := range bad {
		t.Run(b, func(t *testing.T) {
			defer func() {
				if rec := recover(); rec == nil {
					t.Errorf("expected panic for AddMCPTool name=%q", b)
				}
			}()
			New().AddMCPTool(MCPToolDecl{Name: b})
		})
	}
}

func TestMCPResource_AddAndLookup(t *testing.T) {
	t.Parallel()

	r := New()
	r.AddMCPResource(MCPResourceDecl{Name: "status", Description: "Status"})
	if _, ok := r.MCPResource("status"); !ok {
		t.Error("MCPResource(status) not found after add")
	}
	if got := r.MCPResources(); len(got) != 1 {
		t.Errorf("len(MCPResources) = %d, want 1", len(got))
	}
}

func TestMCP_PreservesRegistrationOrder(t *testing.T) {
	t.Parallel()

	r := New()
	r.AddMCPTool(MCPToolDecl{Name: "third"})
	r.AddMCPTool(MCPToolDecl{Name: "first"})
	r.AddMCPTool(MCPToolDecl{Name: "second"})

	got := r.MCPTools()
	if got[0].Name != "third" || got[1].Name != "first" || got[2].Name != "second" {
		t.Errorf("order not preserved: %+v", got)
	}
}
