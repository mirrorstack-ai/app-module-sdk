// Package migration reads the module's sql/ directory and applies up/down
// migrations as a stateless executor.
//
// The SDK does NOT maintain a tracking table and does NOT know which
// migrations were previously applied. The platform's control plane owns that
// state and passes explicit {from, to} ranges to the lifecycle handlers; this
// package just reads the requested slice of SQL files and runs them inside a
// per-migration transaction.
//
// Migrations live under sql/app/ and, optionally, sql/module/. Module
// developers embed both via:
//
//	//go:embed sql/*
//	var sqlFiles embed.FS
//
// and pass sqlFiles to ms.Config.SQL.
package migration

import (
	"io/fs"
	"regexp"
)

// Scope is an independent migration track with its own version sequence.
// Distinct from internal/registry.Scope (which is an auth boundary, not a
// migration directory).
type Scope string

const (
	// ScopeApp reads sql/app/.
	ScopeApp Scope = "app"
	// ScopeModule reads sql/module/.
	ScopeModule Scope = "module"
)

// Dir is the directory under the embedded fs.FS that this scope reads from.
func (s Scope) Dir() string {
	return "sql/" + string(s)
}

// upFilePattern matches up-migration files named like "0000_initial.up.sql".
// The leading numeric prefix is the version, the middle slug is the human name.
var upFilePattern = regexp.MustCompile(`^(\d+)_(.+)\.up\.sql$`)

// downFilePattern matches the corresponding down-migration files.
var downFilePattern = regexp.MustCompile(`^(\d+)_(.+)\.down\.sql$`)

// maxSlugLen caps the human-readable portion of a migration filename. The
// slug has no functional purpose beyond documentation; a 255-byte limit is
// plenty for descriptive names and prevents unbounded slug storage if a
// developer ever ships absurdly long filenames.
const maxSlugLen = 255

// parseUpFilename extracts (version, name) from an up-migration filename.
// Returns ok=false if the filename does not match the expected pattern or
// if the slug portion exceeds maxSlugLen.
func parseUpFilename(name string) (version, slug string, ok bool) {
	m := upFilePattern.FindStringSubmatch(name)
	if m == nil {
		return "", "", false
	}
	if len(m[2]) > maxSlugLen {
		return "", "", false
	}
	return m[1], m[2], true
}

// LatestVersion returns the highest migration version string in the
// sql/{scope}/ directory of fsys, or "" when the directory has no .up.sql
// files. See List for the missing-directory and numeric-comparison contracts.
func LatestVersion(fsys fs.FS, scope Scope) (string, error) {
	migrations, err := List(fsys, scope)
	if err != nil || len(migrations) == 0 {
		return "", err
	}
	// List sorts ascending; the last entry is the highest.
	return migrations[len(migrations)-1].Version, nil
}
