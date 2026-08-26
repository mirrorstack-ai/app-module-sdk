package mirrorstack_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// lineCitation matches a source reference that pins a LINE NUMBER —
// `db.go:103`, `select.go:120-140` — as opposed to one that names a symbol.
var lineCitation = regexp.MustCompile(`[A-Za-z0-9_./-]+\.go:\d+(-\d+)?`)

// Comments must cite SYMBOLS, never line numbers.
//
// 🔴 WHY THIS IS A TEST AND NOT A STYLE PREFERENCE. This repo carried 19 such
// citations and, when each was resolved against the file it named, **14 of
// them pointed at the wrong thing** — blank lines, a fragment of an unrelated
// comment, a closing brace, a logger call inside a different function. They
// had drifted by 1 to 92 lines while reading as authoritative. A reference
// that is confidently wrong is worse than no reference: it sends the next
// reader somewhere real and irrelevant, and nothing about editing the cited
// file makes the citing file's comment update.
//
// `resolvePoolFor (db.go)` costs one grep and cannot rot. `db.go:103` was
// already three lines stale when it was written down.
//
// Test files are scanned too — a rotted comment misleads just as well in a
// test — and the fixture below keeps this honest by proving the matcher fires.
func TestCommentsCiteSymbolsNotLineNumbers(t *testing.T) {
	t.Parallel()

	// Prove the matcher works before trusting a clean result. A regression that
	// makes this pattern match nothing would otherwise read as a pass.
	for _, fixture := range []string{"db.go:103", "select.go:120-140", "system/manifest.go:209"} {
		if !lineCitation.MatchString(fixture) {
			t.Fatalf("the matcher does not fire on %q — a clean run would prove nothing", fixture)
		}
	}
	for _, ok := range []string{"resolvePoolFor (db.go)", "see db.go", "v1.2.3:4"} {
		if lineCitation.MatchString(ok) {
			t.Fatalf("the matcher fires on %q, which cites no line", ok)
		}
	}

	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	var offenders []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// examples/ is its own module with its own CI step; vendor and .git
			// are not ours to lint.
			switch d.Name() {
			case ".git", "vendor", "examples", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "comment_citations_test.go" {
			return nil // this file names the pattern in order to forbid it
		}
		for i, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "//") {
				continue
			}
			if m := lineCitation.FindString(trimmed); m != "" {
				offenders = append(offenders, rel+":"+strconv.Itoa(i+1)+" cites "+m)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range offenders {
		t.Errorf("comment cites a line number: %s — name the symbol instead; line numbers rot silently", o)
	}
}

// findRepoRoot walks up from the test's working directory to the module root.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
