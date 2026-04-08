package mirrorstack

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/system"
)

func TestCron_HandlerReachableViaInternalScope(t *testing.T) {
	m := newTestModuleWithSecret(t, "media")

	called := false
	m.Cron("cleanup-temp", "0 3 * * *", func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	rec := doRequestWithSecret(t, m.Router(), "POST", "/__mirrorstack/crons/cleanup-temp", "secret")
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if !called {
		t.Error("Cron handler was not invoked")
	}
}

func TestCron_RequiresInternalSecret(t *testing.T) {
	// Defense-in-depth: cron handlers are platform-only. If a future
	// refactor moves /__mirrorstack/crons/* to a public scope, this test fails.
	m := newTestModuleWithSecret(t, "media")

	m.Cron("cleanup", "0 3 * * *", func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not run without internal secret")
	})

	rec := doRequest(t, m.Router(), "POST", "/__mirrorstack/crons/cleanup")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (no secret)", rec.Code)
	}
}

func TestCron_AppearsInManifestSchedules(t *testing.T) {
	m := newTestModuleWithSecret(t, "media")

	m.Cron("cleanup-temp", "0 3 * * *", func(w http.ResponseWriter, r *http.Request) {})
	m.Cron("daily-report", "0 9 * * *", func(w http.ResponseWriter, r *http.Request) {})

	rec := doRequestWithSecret(t, m.Router(), "GET", "/__mirrorstack/platform/manifest", "secret")
	var got system.ManifestPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	if len(got.Schedules) != 2 {
		t.Fatalf("schedules = %d, want 2", len(got.Schedules))
	}
	for _, s := range got.Schedules {
		switch s.Name {
		case "cleanup-temp":
			if s.Cron != "0 3 * * *" || s.Path != "/__mirrorstack/crons/cleanup-temp" {
				t.Errorf("cleanup-temp = %+v", s)
			}
		case "daily-report":
			if s.Cron != "0 9 * * *" || s.Path != "/__mirrorstack/crons/daily-report" {
				t.Errorf("daily-report = %+v", s)
			}
		default:
			t.Errorf("unexpected schedule %q", s.Name)
		}
	}
}

func TestCron_PanicsOnDuplicate(t *testing.T) {
	m, _ := New(Config{ID: "media"})
	m.Cron("cleanup", "0 3 * * *", func(w http.ResponseWriter, r *http.Request) {})

	assertPanics(t, "expected panic on duplicate Cron name", func() {
		m.Cron("cleanup", "0 5 * * *", func(w http.ResponseWriter, r *http.Request) {})
	})
}

func TestCron_PanicsOnEmptySchedule(t *testing.T) {
	m, _ := New(Config{ID: "media"})
	assertPanics(t, "expected panic on empty Cron schedule string", func() {
		m.Cron("cleanup", "", func(w http.ResponseWriter, r *http.Request) {})
	})
}

func TestCron_TopLevelPanicsBeforeInit(t *testing.T) {
	resetDefault(t)
	assertPanics(t, "expected panic for top-level Cron before Init", func() {
		Cron("cleanup", "0 3 * * *", func(w http.ResponseWriter, r *http.Request) {})
	})
}

// Note: per-Module registry isolation is already verified by
// TestModulesIsolated_OnEvent in event_test.go and
// TestRequirePermission_AppearsInManifest in mirrorstack_test.go (#28).
// Schedules use the same per-Module Registry, so a separate cron isolation
// test would be redundant.

func TestCron_PanicsOnInvalidName(t *testing.T) {
	// SECURITY regression guard: a name like "../admin" would let chi normalize
	// the registered pattern to "/admin", letting the handler escape the
	// /__mirrorstack/crons/ namespace AND making the manifest disagree with the actual
	// route. The empty case is covered separately by registry validation
	// (also TestCron_PanicsOnEmptyName above for the schedule string).
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
			assertPanics(t, "expected panic for cron name "+tc.bad, func() {
				m.Cron(tc.bad, "0 3 * * *", func(w http.ResponseWriter, r *http.Request) {})
			})
		})
	}
}
