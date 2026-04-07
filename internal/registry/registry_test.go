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
	r.AddSchedule("cleanup-temp", "0 3 * * *")
	r.AddSchedule("daily-report", "0 9 * * *")

	got := r.Schedules()
	if len(got) != 2 {
		t.Fatalf("schedules = %v, want 2", got)
	}
	if got[0].Name != "cleanup-temp" || got[0].Cron != "0 3 * * *" {
		t.Errorf("schedules[0] = %+v, want {cleanup-temp, 0 3 * * *}", got[0])
	}
}

func TestSchedules_DropsDuplicateNames(t *testing.T) {
	t.Parallel()

	// Two schedules with the same name (even with different crons) is a
	// configuration mistake — first-wins matches AddRoute / AddEmit.
	r := New()
	r.AddSchedule("cleanup", "0 3 * * *")
	r.AddSchedule("cleanup", "0 5 * * *")

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
