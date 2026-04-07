// Package migration reads the module's sql/ directory to determine the
// latest migration version. The version is exposed via the manifest endpoint
// so the platform can map module versions to migration numbers on deploy.
//
// Module developers embed their migrations via:
//
//	//go:embed sql/*
//	var sqlFiles embed.FS
//
// and pass sqlFiles to ms.Config.SQL.
package migration

import (
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strconv"
)

// migrationFilePattern matches files named like "0000_initial.up.sql".
// The leading numeric prefix is the version. Down-migration files
// (".down.sql") are intentionally excluded — only up-files determine the
// current schema version.
var migrationFilePattern = regexp.MustCompile(`^(\d+)_.*\.up\.sql$`)

// LatestVersion returns the highest migration version string in the sql/
// directory of fsys. Returns "" if fsys is nil, the sql/ directory does not
// exist, or no .up.sql files are present.
//
// The returned version is the leading numeric prefix as it appears in the
// filename (e.g., "0008") so the platform sees the same string format the
// module developer wrote. Versions are compared numerically — a module that
// mixes widths ("9", "10") still resolves correctly.
func LatestVersion(fsys fs.FS) (string, error) {
	if fsys == nil {
		return "", nil
	}
	entries, err := fs.ReadDir(fsys, "sql")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("mirrorstack/migration: read sql/ dir: %w", err)
	}

	var (
		bestStr string
		bestNum = -1
	)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migrationFilePattern.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if n > bestNum {
			bestNum = n
			bestStr = m[1]
		}
	}
	return bestStr, nil
}
