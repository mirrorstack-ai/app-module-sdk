package sdktest_test

import (
	"net/http"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"

	ms "github.com/mirrorstack-ai/app-module-sdk"
	"github.com/mirrorstack-ai/app-module-sdk/roles"
	"github.com/mirrorstack-ai/app-module-sdk/sdktest"
)

// TestManifestDocExample compiles the snippet documented in the "Testing"
// section of docs/api-reference.md, so the two cannot drift apart.
func TestManifestDocExample(t *testing.T) {
	secret := "test-secret"
	t.Setenv("MS_INTERNAL_SECRET", secret)

	fsys := fstest.MapFS{
		"i18n/en-US.json": &fstest.MapFile{Data: []byte(`{"permissions":{"widgets.view":"View widgets"}}`)},
	}
	if err := ms.RegisterMessages(fsys, "i18n"); err != nil {
		t.Fatalf("RegisterMessages: %v", err)
	}

	if err := ms.Init(ms.Config{ID: "widgets", Name: "Widgets", Icon: "widgets"}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	ms.RegisterPermission("widgets.view", ms.PermissionOpts{
		DefaultRole: roles.Viewer(),
		Label:       ms.T("permissions.widgets.view"),
	})
	ms.Internal(func(r chi.Router) {
		r.With(ms.RequirePermission("widgets.view")).Get("/widgets", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	})

	manifest := sdktest.Manifest(t, ms.DefaultModule().Router(), secret)
	if manifest.ID != "widgets" {
		t.Fatalf("manifest ID = %q, want widgets", manifest.ID)
	}
}
