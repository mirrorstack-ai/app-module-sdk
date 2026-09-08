package core

import (
	"io/fs"
	"strings"
)

// maxReadmeBytes bounds one locale's README. The platform refuses anything
// larger on the version record (readme_too_large), so truncating here keeps a
// dev-mounted module's manifest inside the same limit a published one obeys
// rather than failing later at a boundary the module author cannot see.
const maxReadmeBytes = 65536

// readmeLocales reads a module's long-form README files into a locale map.
//
// 🔴 THE SHAPE MIRRORS THE CLI EXACTLY: "default" for README.md, the locale tag
// for README.<tag>.md. `mirrorstack module deploy` already records this map on
// the version row, and the console resolves it with the same active → en-US →
// default order it uses for every other localized field. A different shape here
// would mean a dev-mounted module rendered under different rules from a
// published one, which is precisely the difference a developer is trying to
// eliminate by running the tunnel.
//
// Returns nil when the module supplies no README, so the manifest omits the key
// rather than shipping an empty object.
func readmeLocales(dir fs.FS) map[string]string {
	if dir == nil {
		return nil
	}
	entries, err := fs.ReadDir(dir, ".")
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		locale, ok := readmeLocale(entry.Name())
		if !ok {
			continue
		}
		content, err := fs.ReadFile(dir, entry.Name())
		if err != nil {
			continue
		}
		text := string(content)
		if len(text) > maxReadmeBytes {
			text = text[:maxReadmeBytes]
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		out[locale] = text
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// readmeLocale maps a file name to the locale it carries.
//
// README.md is the default. README.<tag>.md carries that tag verbatim — the
// console matches on the tag the module wrote, so normalising case here would
// silently stop "zh-TW" matching a zh-TW reader.
func readmeLocale(name string) (string, bool) {
	if name == "README.md" {
		return "default", true
	}
	rest, ok := strings.CutPrefix(name, "README.")
	if !ok {
		return "", false
	}
	tag, ok := strings.CutSuffix(rest, ".md")
	if !ok || tag == "" || strings.Contains(tag, ".") {
		return "", false
	}
	return tag, true
}
