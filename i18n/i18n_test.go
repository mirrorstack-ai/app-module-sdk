package i18n

import (
	"testing"
	"testing/fstest"
)

// loadFixture resets the global catalog and loads en-US + zh-TW from an
// in-memory FS shaped like a real module's i18n/ dir.
func loadFixture(t *testing.T) {
	t.Helper()
	Reset()
	t.Cleanup(Reset)
	fsys := fstest.MapFS{
		"i18n/en-US.json": &fstest.MapFile{Data: []byte(`{"permissions":{"users.read":"View users","users.delete":"Delete users"}}`)},
		"i18n/zh-TW.json": &fstest.MapFile{Data: []byte(`{"permissions":{"users.read":"檢視使用者"}}`)},
		"i18n/README.md":  &fstest.MapFile{Data: []byte("ignored")},
	}
	if err := RegisterMessages(fsys, "i18n"); err != nil {
		t.Fatalf("RegisterMessages: %v", err)
	}
}

func TestRegisterMessages_FlattensNestedKeys(t *testing.T) {
	loadFixture(t)

	got := T("permissions.users.read").Resolve()
	if got["en-US"] != "View users" {
		t.Errorf("en-US = %q, want %q", got["en-US"], "View users")
	}
	if got["zh-TW"] != "檢視使用者" {
		t.Errorf("zh-TW = %q, want %q", got["zh-TW"], "檢視使用者")
	}
}

func TestT_SkipsLocalesMissingKey(t *testing.T) {
	loadFixture(t)

	// users.delete exists only in en-US — zh-TW must be omitted, not blank.
	got := T("permissions.users.delete").Resolve()
	if got["en-US"] != "Delete users" {
		t.Errorf("en-US = %q, want %q", got["en-US"], "Delete users")
	}
	if _, ok := got["zh-TW"]; ok {
		t.Errorf("zh-TW should be omitted for a key it lacks, got %q", got["zh-TW"])
	}
}

func TestT_FallsBackToRawKey(t *testing.T) {
	loadFixture(t)

	// No locale has this key → fall back to {DefaultLocale: rawKey}.
	got := T("permissions.nope").Resolve()
	if len(got) != 1 || got[DefaultLocale] != "permissions.nope" {
		t.Errorf("missing key fallback = %v, want {%s: permissions.nope}", got, DefaultLocale)
	}
}

func TestText_ResolvesUnderDefaultLocale(t *testing.T) {
	loadFixture(t)

	got := Text("Literal label").Resolve()
	if len(got) != 1 || got[DefaultLocale] != "Literal label" {
		t.Errorf("Text resolve = %v, want {%s: Literal label}", got, DefaultLocale)
	}
}

func TestLabel_IsZero(t *testing.T) {
	if !(Label{}).IsZero() {
		t.Error("zero Label should report IsZero()")
	}
	if Text("x").IsZero() {
		t.Error("Text(x) should not be zero")
	}
	if T("k").IsZero() {
		t.Error("T(k) should not be zero")
	}
	// A literal empty Text is still a deliberate value, but IsZero treats it as
	// absent — matching the manifest-omit intent (nothing to show).
	if !Text("").IsZero() {
		t.Error("Text(\"\") is treated as zero for manifest-omit purposes")
	}
}

func TestLookupList_SplitsTrimsAndDropsEmpties(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	fsys := fstest.MapFS{
		// en-US: multi-tag list with irregular spacing + a trailing empty slot.
		"i18n/en-US.json": &fstest.MapFile{Data: []byte(`{"module":{"tags":"Auth,  Payments , "}}`)},
		// zh-TW: single localized tag (no delimiter).
		"i18n/zh-TW.json": &fstest.MapFile{Data: []byte(`{"module":{"tags":"驗證"}}`)},
	}
	if err := RegisterMessages(fsys, "i18n"); err != nil {
		t.Fatalf("RegisterMessages: %v", err)
	}

	got := LookupList("module.tags", ",")
	if want := []string{"Auth", "Payments"}; !equalStrings(got["en-US"], want) {
		t.Errorf("en-US = %v, want %v (split, trimmed, empties dropped)", got["en-US"], want)
	}
	if want := []string{"驗證"}; !equalStrings(got["zh-TW"], want) {
		t.Errorf("zh-TW = %v, want %v", got["zh-TW"], want)
	}
}

func TestLookupList_MissingKeyYieldsEmptyMap(t *testing.T) {
	loadFixture(t) // loads permissions.* only — no module.tags key anywhere.
	if got := LookupList("module.tags", ","); len(got) != 0 {
		t.Errorf("LookupList(missing) = %v, want empty map (no raw-key fallback)", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRegisterMessages_MissingDirErrors(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	if err := RegisterMessages(fstest.MapFS{}, "i18n"); err == nil {
		t.Error("expected error for missing dir")
	}
}
