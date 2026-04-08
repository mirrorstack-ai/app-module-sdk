package system

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
)

func decodeManifest(t *testing.T, h http.HandlerFunc) ManifestPayload {
	t.Helper()
	req := httptest.NewRequest("GET", "/__mirrorstack/platform/manifest", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got ManifestPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	return got
}

func TestManifest_IDAndDefaults(t *testing.T) {
	t.Parallel()

	got := decodeManifest(t, ManifestHandler("media", "Media", "perm_media", nil, nil, registry.New()))

	if got.ID != "media" {
		t.Errorf("id = %q, want media", got.ID)
	}
	if got.Defaults.Name != "Media" {
		t.Errorf("defaults.name = %q, want Media", got.Defaults.Name)
	}
	if got.Defaults.Icon != "perm_media" {
		t.Errorf("defaults.icon = %q, want perm_media", got.Defaults.Icon)
	}
}

func TestManifest_RoutesFromAllScopes(t *testing.T) {
	t.Parallel()

	reg := registry.New()
	reg.AddRoute(registry.ScopePlatform, "GET", "/items")
	reg.AddRoute(registry.ScopePlatform, "POST", "/items")
	reg.AddRoute(registry.ScopePublic, "GET", "/items")
	reg.AddRoute(registry.ScopeInternal, "POST", "/events/on-user-deleted")

	got := decodeManifest(t, ManifestHandler("media", "Media", "perm_media", nil, nil, reg))

	if len(got.Routes[registry.ScopePlatform]) != 2 {
		t.Errorf("platform routes = %d, want 2", len(got.Routes[registry.ScopePlatform]))
	}
	if len(got.Routes[registry.ScopePublic]) != 1 {
		t.Errorf("public routes = %d, want 1", len(got.Routes[registry.ScopePublic]))
	}
	if len(got.Routes[registry.ScopeInternal]) != 1 {
		t.Errorf("internal routes = %d, want 1", len(got.Routes[registry.ScopeInternal]))
	}
}

func TestManifest_EmptyScopesPresent(t *testing.T) {
	t.Parallel()

	got := decodeManifest(t, ManifestHandler("media", "Media", "perm_media", nil, nil, registry.New()))

	for _, scope := range registry.AllScopes() {
		s, ok := got.Routes[scope]
		if !ok {
			t.Errorf("routes.%s missing from manifest, want empty array", scope)
		}
		if s == nil {
			t.Errorf("routes.%s is nil, want empty array []", scope)
		}
	}
}

func TestManifest_EventsAndSchedules(t *testing.T) {
	t.Parallel()

	reg := registry.New()
	reg.AddEmit("created")
	reg.AddSubscribe("oauth.user_deleted", "/internal/events/on-user-deleted")
	reg.AddSchedule("cleanup-temp", "0 3 * * *", "/crons/cleanup-temp")

	got := decodeManifest(t, ManifestHandler("media", "Media", "perm_media", nil, nil, reg))

	if len(got.Events.Emits) != 1 || got.Events.Emits[0] != "created" {
		t.Errorf("events.emits = %v, want [created]", got.Events.Emits)
	}
	if got.Events.Subscribes["oauth.user_deleted"] != "/internal/events/on-user-deleted" {
		t.Errorf("events.subscribes mismatch: %v", got.Events.Subscribes)
	}
	if len(got.Schedules) != 1 || got.Schedules[0].Name != "cleanup-temp" || got.Schedules[0].Path != "/crons/cleanup-temp" {
		t.Errorf("schedules = %v, want [{cleanup-temp 0 3 * * * /crons/cleanup-temp}]", got.Schedules)
	}
}

func TestManifest_EmptyEventsAndSchedules_NotNull(t *testing.T) {
	t.Parallel()

	// Verify the JSON has [] / {} not null when nothing is declared.
	req := httptest.NewRequest("GET", "/__mirrorstack/platform/manifest", nil)
	rec := httptest.NewRecorder()
	ManifestHandler("media", "Media", "perm_media", nil, nil, registry.New()).ServeHTTP(rec, req)

	body := rec.Body.String()
	for _, want := range []string{`"emits":[]`, `"subscribes":{}`, `"schedules":[]`, `"versions":{}`, `"permissions":[]`} {
		if !strings.Contains(body, want) {
			t.Errorf("manifest body missing %q\nbody: %s", want, body)
		}
	}
}

func TestManifest_MigrationVersion(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		"sql/0000_initial.up.sql":   &fstest.MapFile{Data: []byte("")},
		"sql/0008_add_index.up.sql": &fstest.MapFile{Data: []byte("")},
	}

	got := decodeManifest(t, ManifestHandler("media", "Media", "perm_media", fsys, nil, registry.New()))

	if got.Migration != "0008" {
		t.Errorf("migration = %q, want 0008", got.Migration)
	}
}

func TestManifest_NilSQL_EmptyMigration(t *testing.T) {
	t.Parallel()

	got := decodeManifest(t, ManifestHandler("media", "Media", "perm_media", nil, nil, registry.New()))
	if got.Migration != "" {
		t.Errorf("migration = %q, want empty when SQL fs is nil", got.Migration)
	}
}

func TestManifest_Versions(t *testing.T) {
	t.Parallel()

	// Declared versions are surfaced verbatim — this is how the platform
	// learns the semver→migration map it needs to translate deploy requests
	// into the numeric migration IDs the lifecycle handlers require.
	versions := map[string]string{"v0.1.0": "0008", "v0.2.0": "0012"}
	got := decodeManifest(t, ManifestHandler("media", "Media", "perm_media", nil, versions, registry.New()))

	if len(got.Versions) != 2 {
		t.Fatalf("versions = %v, want 2 entries", got.Versions)
	}
	if got.Versions["v0.1.0"] != "0008" || got.Versions["v0.2.0"] != "0012" {
		t.Errorf("versions map mismatch: %v", got.Versions)
	}
}

func TestManifest_Permissions(t *testing.T) {
	t.Parallel()

	// Permissions added on the per-Module registry must surface in the
	// manifest payload. The Registry's first-wins-by-name dedup is the
	// guarantee here — registering the same permission twice (e.g., on two
	// routes that share an authz check) collapses to a single entry.
	reg := registry.New()
	reg.AddPermission("media.view", []string{"admin", "member", "viewer"})
	reg.AddPermission("media.upload", []string{"admin", "member"})
	reg.AddPermission("media.view", []string{"admin"}) // duplicate name → dropped

	got := decodeManifest(t, ManifestHandler("media", "Media", "perm_media", nil, nil, reg))

	if len(got.Permissions) != 2 {
		t.Fatalf("permissions = %d, want 2: %+v", len(got.Permissions), got.Permissions)
	}
	for _, p := range got.Permissions {
		switch p.Name {
		case "media.view":
			if len(p.Roles) != 3 {
				t.Errorf("media.view roles overwritten by duplicate: %v", p.Roles)
			}
		case "media.upload":
			if len(p.Roles) != 2 {
				t.Errorf("media.upload roles wrong: %v", p.Roles)
			}
		default:
			t.Errorf("unexpected permission %q", p.Name)
		}
	}
}
