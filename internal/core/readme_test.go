package core

import (
	"strings"
	"testing"
	"testing/fstest"
)

// 🔴 THE MAP SHAPE IS A CONTRACT WITH TWO OTHER PROGRAMS, not an internal
// detail. `mirrorstack module deploy` records exactly this shape on a published
// version, and the console resolves it with one candidate order for every
// localized field. A dev-mounted module that shipped a different shape would
// render under different rules from a published one — which is the difference a
// developer runs the tunnel to eliminate.
func TestReadmeLocales(t *testing.T) {
	readme := readmeLocales(fstest.MapFS{
		"README.md":       {Data: []byte("# User Core\n")},
		"README.zh-TW.md": {Data: []byte("# 使用者核心\n")},
		"README.ja.md":    {Data: []byte("# ユーザーコア\n")},
	})

	if got := readme["default"]; got != "# User Core\n" {
		t.Errorf("default = %q, want the plain README.md", got)
	}
	if got := readme["zh-TW"]; got != "# 使用者核心\n" {
		t.Errorf("zh-TW = %q, want README.zh-TW.md", got)
	}
	// The tag is carried VERBATIM. Normalising case here would silently stop
	// "zh-TW" matching a zh-TW reader, and nothing downstream would say so.
	if _, ok := readme["zh-tw"]; ok {
		t.Error("locale tag was case-folded; the console matches the tag as written")
	}
	if len(readme) != 3 {
		t.Errorf("locales = %d, want 3", len(readme))
	}
}

// A module with no README must ship NO key rather than an empty object: the
// manifest field is omitempty so the console can tell "declared nothing" from
// "declared an empty README", and fall back to the recorded version for the
// first but not the second.
func TestReadmeLocalesAbsent(t *testing.T) {
	if got := readmeLocales(nil); got != nil {
		t.Errorf("nil FS = %v, want nil", got)
	}
	if got := readmeLocales(fstest.MapFS{}); got != nil {
		t.Errorf("empty FS = %v, want nil", got)
	}
	// A file that only LOOKS like a README contributes nothing.
	if got := readmeLocales(fstest.MapFS{
		"READMEnot.md":  {Data: []byte("x")},
		"README.txt":    {Data: []byte("x")},
		"README..md":    {Data: []byte("x")},
		"README.a.b.md": {Data: []byte("x")},
	}); got != nil {
		t.Errorf("non-README files = %v, want nil", got)
	}
}

// A blank README is a placeholder, not content. Carrying it would push an empty
// panel into the console in place of the recorded text it would otherwise fall
// back to — strictly worse than declaring nothing.
func TestReadmeLocalesSkipsBlank(t *testing.T) {
	readme := readmeLocales(fstest.MapFS{
		"README.md":       {Data: []byte("   \n\t\n")},
		"README.zh-TW.md": {Data: []byte("# 有內容\n")},
	})
	if _, ok := readme["default"]; ok {
		t.Error("a whitespace-only README was carried")
	}
	// The positive control: the real one beside it still arrives, so this is
	// not passing because everything was dropped.
	if readme["zh-TW"] != "# 有內容\n" {
		t.Errorf("zh-TW = %q, want the non-blank README", readme["zh-TW"])
	}
}

// The platform refuses an oversized readme on the version record
// (readme_too_large). Bounding it here keeps a dev-mounted module inside the
// same limit a published one obeys, rather than failing later at a boundary the
// module author never sees.
func TestReadmeLocalesBounded(t *testing.T) {
	readme := readmeLocales(fstest.MapFS{
		"README.md": {Data: []byte(strings.Repeat("a", maxReadmeBytes+512))},
	})
	if got := len(readme["default"]); got != maxReadmeBytes {
		t.Errorf("length = %d, want it truncated to %d", got, maxReadmeBytes)
	}
}
