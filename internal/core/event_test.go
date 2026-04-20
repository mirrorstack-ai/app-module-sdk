package core

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/system"
)

func TestOnEvent_HandlerReachableViaInternalScope(t *testing.T) {
	m := newTestModuleWithSecret(t, "media")

	called := false
	m.OnEvent("oauth.user_deleted", func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	rec := doRequestWithSecret(t, m.Router(), "POST", "/__mirrorstack/events/oauth.user_deleted", "secret")
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if !called {
		t.Error("OnEvent handler was not invoked")
	}
}

func TestOnEvent_RequiresInternalSecret(t *testing.T) {
	// Defense-in-depth: events are mounted on the Internal scope, so the
	// InternalAuth gate must reject unauthenticated callers. If a future
	// refactor accidentally moves OnEvent to a public-scope mount, this
	// test fails immediately.
	m := newTestModuleWithSecret(t, "media")

	m.OnEvent("user.created", func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not run without internal secret")
	})

	rec := doRequest(t, m.Router(), "POST", "/__mirrorstack/events/user.created")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (no secret)", rec.Code)
	}
}

func TestOnEvent_AppearsInManifestSubscribes(t *testing.T) {
	m := newTestModuleWithSecret(t, "media")

	m.OnEvent("oauth.user_deleted", func(w http.ResponseWriter, r *http.Request) {})
	m.OnEvent("billing.payment_succeeded", func(w http.ResponseWriter, r *http.Request) {})

	rec := doRequestWithSecret(t, m.Router(), "GET", "/__mirrorstack/platform/manifest", "secret")
	var got system.ManifestPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	if got.Events.Subscribes["oauth.user_deleted"] != "/__mirrorstack/events/oauth.user_deleted" {
		t.Errorf("subscribes[oauth.user_deleted] = %q, want /__mirrorstack/events/oauth.user_deleted", got.Events.Subscribes["oauth.user_deleted"])
	}
	if got.Events.Subscribes["billing.payment_succeeded"] != "/__mirrorstack/events/billing.payment_succeeded" {
		t.Errorf("subscribes[billing.payment_succeeded] = %q", got.Events.Subscribes["billing.payment_succeeded"])
	}
}

func TestOnEvent_PanicsOnDuplicate(t *testing.T) {
	m, _ := New(Config{ID: "media"})
	m.OnEvent("user.created", func(w http.ResponseWriter, r *http.Request) {})

	assertPanics(t, "expected panic on duplicate OnEvent", func() {
		m.OnEvent("user.created", func(w http.ResponseWriter, r *http.Request) {})
	})
}

func TestEmits_AppearsInManifestEmits(t *testing.T) {
	m := newTestModuleWithSecret(t, "media")

	m.Emits("created")
	m.Emits("deleted")

	rec := doRequestWithSecret(t, m.Router(), "GET", "/__mirrorstack/platform/manifest", "secret")
	var got system.ManifestPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	if len(got.Events.Emits) != 2 {
		t.Fatalf("emits = %v, want 2", got.Events.Emits)
	}
	want := map[string]bool{"created": true, "deleted": true}
	for _, e := range got.Events.Emits {
		if !want[e] {
			t.Errorf("unexpected emit %q", e)
		}
	}
}

func TestEmits_PanicsOnDuplicate(t *testing.T) {
	m, _ := New(Config{ID: "media"})
	m.Emits("created")

	assertPanics(t, "expected panic on duplicate Emits", func() {
		m.Emits("created")
	})
}

func TestEvent_TopLevelPanicsBeforeInit(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func()
	}{
		{"OnEvent", func() { OnEvent("user.created", func(w http.ResponseWriter, r *http.Request) {}) }},
		{"Emits", func() { Emits("created") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetDefault(t)
			assertPanics(t, "expected panic for top-level "+tc.name+" before Init", tc.fn)
		})
	}
}

func TestOnEvent_PanicsOnInvalidName(t *testing.T) {
	// SECURITY regression guard: a name like "../admin" would let chi normalize
	// the registered pattern to "/admin", letting the handler escape the
	// /__mirrorstack/events/ namespace AND making the manifest disagree with the actual
	// route. validateRegistrationName (in internal/registry) blocks this at
	// the API boundary.
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
			m, _ := New(Config{ID: "media"})
			assertPanics(t, "expected panic for event name "+tc.bad, func() {
				m.OnEvent(tc.bad, func(w http.ResponseWriter, r *http.Request) {})
			})
		})
	}
}

func TestEmits_PanicsOnInvalidName(t *testing.T) {
	// Emits doesn't mount a route, but its name is still developer-facing in
	// the manifest payload — same validation rules apply for consistency
	// and to catch typos at startup. Subset of the OnEvent matrix.
	cases := []struct {
		name string
		bad  string
	}{
		{"empty", ""},
		{"dot-segment", "../admin"},
		{"slash", "foo/bar"},
		{"backslash", "foo\\bar"},
		{"whitespace", "foo bar"},
		{"newline", "foo\nbar"},
		{"null-byte", "foo\x00bar"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := New(Config{ID: "media"})
			assertPanics(t, "expected panic for emits name "+tc.bad, func() {
				m.Emits(tc.bad)
			})
		})
	}
}

func TestModulesIsolated_OnEvent(t *testing.T) {
	// Two modules in the same process must not see each other's event
	// subscriptions in the manifest. Mirrors the test isolation guarantee
	// from #28 (per-Module registry, no package globals).
	m1 := newTestModuleWithSecret(t, "media")
	m2, _ := New(Config{ID: "billing"})

	m1.OnEvent("oauth.user_deleted", func(w http.ResponseWriter, r *http.Request) {})
	m2.OnEvent("media.uploaded", func(w http.ResponseWriter, r *http.Request) {})

	rec := doRequestWithSecret(t, m1.Router(), "GET", "/__mirrorstack/platform/manifest", "secret")
	var got system.ManifestPayload
	_ = json.Unmarshal(rec.Body.Bytes(), &got)

	if _, ok := got.Events.Subscribes["media.uploaded"]; ok {
		t.Error("m1 manifest leaked m2's event subscription")
	}
	if _, ok := got.Events.Subscribes["oauth.user_deleted"]; !ok {
		t.Error("m1 manifest missing its own subscription")
	}
}
