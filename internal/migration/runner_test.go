package migration

import (
	"testing"
	"testing/fstest"
)

func TestList_NilFS(t *testing.T) {
	t.Parallel()

	got, err := List(nil)
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d migrations, want 0", len(got))
	}
}

func TestList_NoSQLDir(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{"README.md": &fstest.MapFile{Data: []byte("hello")}}
	got, err := List(fsys)
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d migrations, want 0", len(got))
	}
}

func TestList_PairsUpAndDown(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		"sql/0000_initial.up.sql":     &fstest.MapFile{Data: []byte("CREATE TABLE items()")},
		"sql/0000_initial.down.sql":   &fstest.MapFile{Data: []byte("DROP TABLE items")},
		"sql/0001_add_title.up.sql":   &fstest.MapFile{Data: []byte("ALTER TABLE items ADD title TEXT")},
		"sql/0001_add_title.down.sql": &fstest.MapFile{Data: []byte("ALTER TABLE items DROP COLUMN title")},
	}

	got, err := List(fsys)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d migrations, want 2", len(got))
	}
	if got[0].Version != "0000" || got[0].Name != "initial" {
		t.Errorf("migration 0: got %+v, want version=0000 name=initial", got[0])
	}
	if got[0].UpFile != "sql/0000_initial.up.sql" || got[0].DownFile != "sql/0000_initial.down.sql" {
		t.Errorf("migration 0 file paths wrong: %+v", got[0])
	}
	if got[1].Version != "0001" || got[1].Name != "add_title" {
		t.Errorf("migration 1: got %+v, want version=0001 name=add_title", got[1])
	}
}

func TestList_AllowsMissingDownFile(t *testing.T) {
	t.Parallel()

	// Some migrations are intentionally one-way (e.g., data backfills).
	// List records DownFile="" — ApplyDown later returns an error if asked.
	fsys := fstest.MapFS{
		"sql/0001_one_way.up.sql": &fstest.MapFile{Data: []byte("INSERT INTO ...")},
	}
	got, _ := List(fsys)
	if len(got) != 1 {
		t.Fatalf("got %d migrations, want 1", len(got))
	}
	if got[0].DownFile != "" {
		t.Errorf("DownFile = %q, want empty", got[0].DownFile)
	}
}

func TestList_SortsNumericallyNotLexically(t *testing.T) {
	t.Parallel()

	// Regression: a string sort would put "9" after "10". Numeric comparison
	// is required so mixed-width version numbers resolve in the right order.
	fsys := fstest.MapFS{
		"sql/9_old.up.sql":     &fstest.MapFile{Data: []byte("")},
		"sql/10_new.up.sql":    &fstest.MapFile{Data: []byte("")},
		"sql/100_newer.up.sql": &fstest.MapFile{Data: []byte("")},
	}
	got, _ := List(fsys)
	if len(got) != 3 {
		t.Fatalf("got %d migrations, want 3", len(got))
	}
	if got[0].Version != "9" || got[1].Version != "10" || got[2].Version != "100" {
		t.Errorf("order = %v %v %v, want 9 10 100", got[0].Version, got[1].Version, got[2].Version)
	}
}

func TestList_IgnoresNonMigrationFiles(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		"sql/0001_a.up.sql":          &fstest.MapFile{Data: []byte("")},
		"sql/README.md":              &fstest.MapFile{Data: []byte("")},
		"sql/queries/list_items.sql": &fstest.MapFile{Data: []byte("")}, // sqlc query (not a migration)
	}
	got, _ := List(fsys)
	if len(got) != 1 {
		t.Errorf("got %d migrations, want 1 (non-migration files ignored)", len(got))
	}
}

func TestSlice_Install(t *testing.T) {
	t.Parallel()

	migrations := []Migration{
		{Version: "0000"},
		{Version: "0001"},
		{Version: "0002"},
	}
	// Install: from "" (before everything) to "0002" → all migrations
	got, err := Slice(migrations, "", "0002")
	if err != nil {
		t.Fatalf("Slice: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("install slice = %d, want 3", len(got))
	}
}

func TestSlice_Upgrade(t *testing.T) {
	t.Parallel()

	migrations := []Migration{
		{Version: "0000"},
		{Version: "0001"},
		{Version: "0002"},
		{Version: "0003"},
	}
	// Upgrade from 0001 to 0003 → migrations 0002, 0003 (exclusive of from, inclusive of to)
	got, err := Slice(migrations, "0001", "0003")
	if err != nil {
		t.Fatalf("Slice: %v", err)
	}
	if len(got) != 2 || got[0].Version != "0002" || got[1].Version != "0003" {
		t.Errorf("upgrade slice = %v, want [0002, 0003]", versions(got))
	}
}

func TestSlice_NoOp(t *testing.T) {
	t.Parallel()

	migrations := []Migration{{Version: "0000"}, {Version: "0001"}}
	// Already at the target → empty
	got, err := Slice(migrations, "0001", "0001")
	if err != nil {
		t.Fatalf("Slice: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("no-op slice = %v, want empty", versions(got))
	}
}

func TestSlice_UnknownTarget(t *testing.T) {
	t.Parallel()

	migrations := []Migration{{Version: "0000"}, {Version: "0001"}}
	if _, err := Slice(migrations, "0000", "9999"); err == nil {
		t.Error("expected error for unknown target version")
	}
}

func TestSlice_InvalidVersion(t *testing.T) {
	t.Parallel()

	migrations := []Migration{{Version: "0000"}}
	if _, err := Slice(migrations, "abc", "0000"); err == nil {
		t.Error("expected error for non-numeric version")
	}
}

func TestSliceDown_ReversesOrder(t *testing.T) {
	t.Parallel()

	migrations := []Migration{
		{Version: "0000", DownFile: "sql/0000_a.down.sql"},
		{Version: "0001", DownFile: "sql/0001_b.down.sql"},
		{Version: "0002", DownFile: "sql/0002_c.down.sql"},
		{Version: "0003", DownFile: "sql/0003_d.down.sql"},
	}
	// Downgrade from 0003 to 0001 → revert 0003, then 0002 (reverse order)
	got, err := SliceDown(migrations, "0003", "0001")
	if err != nil {
		t.Fatalf("SliceDown: %v", err)
	}
	if len(got) != 2 || got[0].Version != "0003" || got[1].Version != "0002" {
		t.Errorf("downgrade slice = %v, want [0003, 0002]", versions(got))
	}
}

func TestSliceDown_RequiresFromGreaterThanTo(t *testing.T) {
	t.Parallel()

	migrations := []Migration{{Version: "0000"}, {Version: "0001"}}
	if _, err := SliceDown(migrations, "0000", "0001"); err == nil {
		t.Error("expected error when from <= to")
	}
}

// Apply / ApplyDown / EnsureTrackingTable / AppliedVersions are tested via
// integration tests against a real Postgres in db_integration_test.go and via
// the end-to-end lifecycle tests in mirrorstack_test.go.

func versions(migs []Migration) []string {
	out := make([]string, len(migs))
	for i, m := range migs {
		out[i] = m.Version
	}
	return out
}
