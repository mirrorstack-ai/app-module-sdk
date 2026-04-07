package system

import (
	"encoding/json"
	"io/fs"
	"net/http"

	"github.com/mirrorstack-ai/app-module-sdk/internal/httputil"
	"github.com/mirrorstack-ai/app-module-sdk/internal/migration"
)

// LifecycleResult is the JSON response for install/upgrade/downgrade.
// Skipped is omitted from upgrade/downgrade responses (only install is idempotent).
type LifecycleResult struct {
	Applied  []string `json:"applied,omitempty"`
	Reverted []string `json:"reverted,omitempty"`
	Skipped  []string `json:"skipped,omitempty"`
}

// LifecycleError is the JSON shape for partial-failure responses: it carries
// whatever the runner managed to apply/revert before the failure plus the
// error message. Used by install/upgrade/downgrade so the platform can see
// which migrations succeeded before retrying.
type LifecycleError struct {
	LifecycleResult
	Error string `json:"error"`
}

// VersionRequest is the body shape for upgrade and downgrade.
type VersionRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// UninstallResult is the response for uninstall.
type UninstallResult struct {
	Status string `json:"status"`
}

// InstallHandler returns an http.HandlerFunc that applies all migrations
// from sqlFS in numeric ascending order. Idempotent — already-applied
// migrations are skipped via the __mirrorstack_migrations tracking table.
func InstallHandler(sqlFS fs.FS, runTx migration.TxRunner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		migrations, err := migration.List(sqlFS)
		if err != nil {
			httputil.JSON(w, http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
			return
		}
		applied, skipped, err := migration.Apply(r.Context(), runTx, sqlFS, migrations)
		if err != nil {
			// Partial progress: report what was applied + the error.
			httputil.JSON(w, http.StatusInternalServerError, LifecycleError{
				LifecycleResult: LifecycleResult{Applied: applied, Skipped: skipped},
				Error:           err.Error(),
			})
			return
		}
		httputil.JSON(w, http.StatusOK, LifecycleResult{Applied: applied, Skipped: skipped})
	}
}

// UpgradeHandler applies the migrations strictly between (req.From, req.To],
// resolving semver to migration numbers via the versions map (semver →
// migration number). If versions is nil or req.From/req.To are not in it,
// the values are used as migration numbers directly.
func UpgradeHandler(sqlFS fs.FS, versions map[string]string, runTx migration.TxRunner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := decodeVersionRequest(w, r)
		if !ok {
			return
		}
		fromV := resolveVersion(versions, req.From)
		toV := resolveVersion(versions, req.To)

		migrations, err := migration.List(sqlFS)
		if err != nil {
			httputil.JSON(w, http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
			return
		}
		slice, err := migration.Slice(migrations, fromV, toV)
		if err != nil {
			httputil.JSON(w, http.StatusBadRequest, httputil.ErrorResponse{Error: err.Error()})
			return
		}

		applied, skipped, err := migration.Apply(r.Context(), runTx, sqlFS, slice)
		if err != nil {
			httputil.JSON(w, http.StatusInternalServerError, LifecycleError{
				LifecycleResult: LifecycleResult{Applied: applied, Skipped: skipped},
				Error:           err.Error(),
			})
			return
		}
		httputil.JSON(w, http.StatusOK, LifecycleResult{Applied: applied, Skipped: skipped})
	}
}

// DowngradeHandler reverts migrations between (req.To, req.From] in newest-first
// order. Each migration must have a .down.sql or the request fails before any
// SQL runs. Like UpgradeHandler, semver inputs are resolved via the versions map.
func DowngradeHandler(sqlFS fs.FS, versions map[string]string, runTx migration.TxRunner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := decodeVersionRequest(w, r)
		if !ok {
			return
		}
		fromV := resolveVersion(versions, req.From)
		toV := resolveVersion(versions, req.To)

		migrations, err := migration.List(sqlFS)
		if err != nil {
			httputil.JSON(w, http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
			return
		}
		slice, err := migration.SliceDown(migrations, fromV, toV)
		if err != nil {
			httputil.JSON(w, http.StatusBadRequest, httputil.ErrorResponse{Error: err.Error()})
			return
		}

		reverted, err := migration.ApplyDown(r.Context(), runTx, sqlFS, slice)
		if err != nil {
			httputil.JSON(w, http.StatusInternalServerError, LifecycleError{
				LifecycleResult: LifecycleResult{Reverted: reverted},
				Error:           err.Error(),
			})
			return
		}
		httputil.JSON(w, http.StatusOK, LifecycleResult{Reverted: reverted})
	}
}

// UninstallHandler is a no-op that returns success. The platform handles
// soft-delete and retention separately — modules are not asked to drop their
// own data, because that would prevent restore.
func UninstallHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		httputil.JSON(w, http.StatusOK, UninstallResult{Status: "ok"})
	}
}

// resolveVersion converts a semver-like string (e.g., "v0.1.0") into a
// migration number string (e.g., "0008"). If versions doesn't contain the
// input, returns the input unchanged — callers may pass migration numbers
// directly when no semver mapping is configured.
func resolveVersion(versions map[string]string, input string) string {
	if v, ok := versions[input]; ok {
		return v
	}
	return input
}

// decodeVersionRequest reads and validates the {from, to} body. Returns ok=false
// after writing a 400 response if the body is missing or malformed.
func decodeVersionRequest(w http.ResponseWriter, r *http.Request) (VersionRequest, bool) {
	var req VersionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.JSON(w, http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid request body: " + err.Error()})
		return req, false
	}
	if req.From == "" || req.To == "" {
		httputil.JSON(w, http.StatusBadRequest, httputil.ErrorResponse{Error: "from and to are required"})
		return req, false
	}
	return req, true
}
