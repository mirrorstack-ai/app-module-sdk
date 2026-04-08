package migration

import (
	"testing"
	"testing/fstest"
)

func TestLatestVersion_NilFS(t *testing.T) {
	t.Parallel()

	got, err := LatestVersion(nil, ScopeApp)
	if err != nil {
		t.Errorf("LatestVersion(nil) err = %v, want nil", err)
	}
	if got != "" {
		t.Errorf("LatestVersion(nil) = %q, want empty", got)
	}
}

func TestLatestVersion_NoSQLDir(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		"README.md": &fstest.MapFile{Data: []byte("hello")},
	}
	got, err := LatestVersion(fsys, ScopeApp)
	if err != nil {
		t.Errorf("err = %v, want nil for missing sql/app/ dir", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty when sql/app/ dir absent", got)
	}
}

func TestLatestVersion_PicksHighest(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		"sql/app/0000_initial.up.sql":     &fstest.MapFile{Data: []byte("CREATE TABLE items (id SERIAL);")},
		"sql/app/0000_initial.down.sql":   &fstest.MapFile{Data: []byte("DROP TABLE items;")},
		"sql/app/0001_add_title.up.sql":   &fstest.MapFile{Data: []byte("ALTER TABLE items ADD title TEXT;")},
		"sql/app/0001_add_title.down.sql": &fstest.MapFile{Data: []byte("ALTER TABLE items DROP COLUMN title;")},
		"sql/app/0008_add_index.up.sql":   &fstest.MapFile{Data: []byte("CREATE INDEX ON items(title);")},
		"sql/app/0008_add_index.down.sql": &fstest.MapFile{Data: []byte("DROP INDEX items_title_idx;")},
	}
	got, err := LatestVersion(fsys, ScopeApp)
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if got != "0008" {
		t.Errorf("LatestVersion = %q, want 0008", got)
	}
}

func TestLatestVersion_IgnoresDownFiles(t *testing.T) {
	t.Parallel()

	// Even if a down-only file has a higher version, it shouldn't count.
	fsys := fstest.MapFS{
		"sql/app/0001_a.up.sql":   &fstest.MapFile{Data: []byte("")},
		"sql/app/0009_b.down.sql": &fstest.MapFile{Data: []byte("")},
	}
	got, _ := LatestVersion(fsys, ScopeApp)
	if got != "0001" {
		t.Errorf("got %q, want 0001 (.down.sql files must be ignored)", got)
	}
}

func TestLatestVersion_IgnoresNonMigrationFiles(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		"sql/app/0001_a.up.sql":    &fstest.MapFile{Data: []byte("")},
		"sql/app/README.md":        &fstest.MapFile{Data: []byte("")},
		"sql/app/queries/list.sql": &fstest.MapFile{Data: []byte("")}, // sqlc query file (not a migration)
	}
	got, _ := LatestVersion(fsys, ScopeApp)
	if got != "0001" {
		t.Errorf("got %q, want 0001 (non-migration files must be ignored)", got)
	}
}

func TestLatestVersion_EmptySQLDir(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		"sql/app/.gitkeep": &fstest.MapFile{Data: []byte("")},
	}
	got, err := LatestVersion(fsys, ScopeApp)
	if err != nil {
		t.Errorf("err = %v, want nil for empty sql/app/ dir", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty for empty sql/app/ dir", got)
	}
}

func TestLatestVersion_PreservesZeroPadding(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		"sql/app/0042_a.up.sql": &fstest.MapFile{Data: []byte("")},
	}
	got, _ := LatestVersion(fsys, ScopeApp)
	if got != "0042" {
		t.Errorf("got %q, want 0042 (must preserve zero-padding from filename)", got)
	}
}

func TestLatestVersion_MixedWidthsSortNumerically(t *testing.T) {
	t.Parallel()

	// Regression: a string sort would put "9" after "10" because '9' > '1'.
	// LatestVersion must compare numerically so a module that mixes widths
	// still resolves the highest version correctly.
	fsys := fstest.MapFS{
		"sql/app/9_old.up.sql":  &fstest.MapFile{Data: []byte("")},
		"sql/app/10_new.up.sql": &fstest.MapFile{Data: []byte("")},
	}
	got, _ := LatestVersion(fsys, ScopeApp)
	if got != "10" {
		t.Errorf("got %q, want 10 (numeric comparison, not string sort)", got)
	}
}

// --- ScopeModule track ---
//
// The module track is the new sql/module/ directory introduced in #55.
// All scope-aware behavior must work for ScopeModule too — the tests below
// mirror the ScopeApp matrix at a representative depth so a regression in
// the scope-parameterization branch fires loudly.

func TestLatestVersion_ModuleScope_NoDir(t *testing.T) {
	t.Parallel()

	// A module that has only an app track (no cross-app shared state) is
	// the common case. ModuleScope must report "" without error.
	fsys := fstest.MapFS{
		"sql/app/0001_a.up.sql": &fstest.MapFile{Data: []byte("")},
	}
	got, err := LatestVersion(fsys, ScopeModule)
	if err != nil {
		t.Errorf("ScopeModule with no sql/module/ dir: err = %v, want nil", err)
	}
	if got != "" {
		t.Errorf("ScopeModule with no sql/module/ dir: got %q, want empty", got)
	}
}

func TestLatestVersion_ModuleScope_PicksHighest(t *testing.T) {
	t.Parallel()

	// Both tracks coexist; ModuleScope must read its own dir, not app/.
	fsys := fstest.MapFS{
		"sql/app/0000_initial.up.sql":   &fstest.MapFile{Data: []byte("")},
		"sql/app/0008_app_thing.up.sql": &fstest.MapFile{Data: []byte("")},
		"sql/module/0000_outbox.up.sql": &fstest.MapFile{Data: []byte("")},
		"sql/module/0001_dedup.up.sql":  &fstest.MapFile{Data: []byte("")},
	}
	got, err := LatestVersion(fsys, ScopeModule)
	if err != nil {
		t.Fatalf("LatestVersion(ScopeModule): %v", err)
	}
	if got != "0001" {
		t.Errorf("LatestVersion(ScopeModule) = %q, want 0001 (must NOT see sql/app/ entries)", got)
	}

	// Sanity: the app scope still sees its own files.
	gotApp, _ := LatestVersion(fsys, ScopeApp)
	if gotApp != "0008" {
		t.Errorf("LatestVersion(ScopeApp) = %q, want 0008 (must NOT see sql/module/ entries)", gotApp)
	}
}

func TestScope_Dir(t *testing.T) {
	t.Parallel()

	if got := ScopeApp.Dir(); got != "sql/app" {
		t.Errorf("ScopeApp.Dir() = %q, want sql/app", got)
	}
	if got := ScopeModule.Dir(); got != "sql/module" {
		t.Errorf("ScopeModule.Dir() = %q, want sql/module", got)
	}
}
