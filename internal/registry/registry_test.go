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
	// validateRegistrationName guard with AddSubscribe/AddEmit/AddSchedule
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
