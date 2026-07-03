package system

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strconv"

	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/httputil"
	"github.com/mirrorstack-ai/app-module-sdk/internal/migration"
)

// LifecycleResult is the JSON response for install/upgrade/downgrade. Install
// and upgrade return Applied; downgrade returns Reverted.
type LifecycleResult struct {
	Applied  []string `json:"applied,omitempty"`
	Reverted []string `json:"reverted,omitempty"`
}

// LifecycleError is the JSON shape for partial-failure responses: it carries
// whatever the runner managed to apply/revert before the failure plus the
// error message. The platform uses this to update its own state with the
// versions that actually ran before retrying.
type LifecycleError struct {
	LifecycleResult
	Error string `json:"error"`
}

// VersionRequest is the migration window for upgrade and downgrade. Both
// fields MUST be numeric migration numbers ("0008"), not semver strings
// ("v0.1.0"). The platform does semver→migration translation before calling
// the SDK, using the Versions map exposed via the manifest endpoint.
type VersionRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// UpgradeRequest is the full body shape for upgrade and downgrade: the
// migration window plus the same optional install context the platform sends
// on install (appId, schema, credential). It mirrors api-platform's
// upgradeRequest verbatim — the wire shape is the contract.
//
// A bare {from, to} body (the dev-tunnel path, where the module runs against
// its own env DB) keeps the pre-existing behavior: no schema or credential is
// injected and migrations run on the SDK's DATABASE_URL pool. When the
// platform drives a DEPLOYED (non-tunnel) upgrade it populates Schema +
// Credential so the (from, to] migrations run under the per-(app, module)
// r_* role against the app schema — exactly like install.
type UpgradeRequest struct {
	VersionRequest
	InstallRequest
}

// InstallRequest is the optional body for the install endpoint. The
// platform populates Credential + Schema so the SDK runs migrations
// under the per-(app, module) role provisioned at install time; AppID
// is metadata for module-side logging/auditing.
//
// All fields are optional — an empty body falls back to the dev path
// (DATABASE_URL pool, default schema), matching the pre-B2b behavior
// that `mirrorstack dev`'s migration auto-apply relies on.
type InstallRequest struct {
	AppID      string             `json:"appId,omitempty"`
	Schema     string             `json:"schema,omitempty"`
	Credential *InstallCredential `json:"credential,omitempty"`
}

// InstallCredential is the per-install Postgres credential delivered in
// the install request body. Only Username + Token live here — those are
// the per-(app, module) values only the platform knows (minted when the
// per-module role was created).
//
// Host/Port/Database deliberately do NOT live here. They come from the
// module's own environment (db.EnvBaseCredential — backed by
// MS_LOCAL_DB_URL / DATABASE_URL in dev, AWS Secrets Manager in
// production) so the platform doesn't leak its DB location to every
// install payload.
type InstallCredential struct {
	Username string `json:"username"`
	Token    string `json:"token"`
}

// UninstallResult is the response for uninstall.
type UninstallResult struct {
	Status string `json:"status"`
}

// InstallHandler returns an http.HandlerFunc that applies all migrations
// from sqlFS in numeric ascending order.
//
// The SDK does NOT track which migrations were previously applied — it is a
// stateless executor. The platform decides when install is appropriate (a
// fresh app, or first deploy of a module) based on its own state store and
// calls install at most once per (scope, target). Calling install twice
// will re-run every migration and likely fail with "relation already exists"
// — platform-side saga logic is responsible for preventing that.
func InstallHandler(sqlFS fs.FS, scope migration.Scope, runTx migration.TxRunner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, ok := injectInstallContext(w, r)
		if !ok {
			return
		}
		migrations, err := migration.List(sqlFS, scope)
		if err != nil {
			httputil.JSON(w, http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
			return
		}
		applied, err := migration.Apply(ctx, runTx, sqlFS, migrations)
		if err != nil {
			httputil.JSON(w, http.StatusInternalServerError, LifecycleError{
				LifecycleResult: LifecycleResult{Applied: applied},
				Error:           err.Error(),
			})
			return
		}
		httputil.JSON(w, http.StatusOK, LifecycleResult{Applied: applied})
	}
}

// injectInstallContext reads an optional InstallRequest body and folds its
// Credential + Schema into the request context. Empty body is allowed —
// the dev/legacy path keeps working with no body at all, falling through
// to the SDK's DATABASE_URL pool when nothing is in ctx.
//
// Returns ok=false after writing the response (400 or 413) when the body
// is present but malformed; callers must return without touching w again.
func injectInstallContext(w http.ResponseWriter, r *http.Request) (context.Context, bool) {
	ctx := r.Context()
	var req InstallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if errors.Is(err, io.EOF) {
			return ctx, true
		}
		writeBodyDecodeError(w, err)
		return ctx, false
	}
	return injectLifecycleContext(w, ctx, req)
}

// injectLifecycleContext folds an already-decoded install context (Schema +
// Credential) into ctx. It is the single implementation shared by install
// (whole body) and upgrade/downgrade (embedded in UpgradeRequest) so the
// credential semantics cannot drift between the lifecycle verbs.
//
// Returns ok=false after writing a 500 when the env DB base cannot be
// resolved; callers must return without touching w again.
func injectLifecycleContext(w http.ResponseWriter, ctx context.Context, req InstallRequest) (context.Context, bool) {
	if req.Schema != "" {
		ctx = db.WithSchema(ctx, req.Schema)
	}
	// Treat empty username AND empty token as "no credential supplied" —
	// caller sent `"credential":{}` for shape compat but the dev pool
	// fallback should still apply. Avoids injecting a half-credential that
	// would later trip resolvePool's "credential missing required fields"
	// validation.
	if req.Credential != nil && (req.Credential.Username != "" || req.Credential.Token != "") {
		base, err := db.EnvBaseCredential()
		if err != nil {
			httputil.JSON(w, http.StatusInternalServerError, httputil.ErrorResponse{Error: "resolve DB env base: " + err.Error()})
			return ctx, false
		}
		base.Username = req.Credential.Username
		base.Token = req.Credential.Token
		ctx = db.WithCredential(ctx, base)
	}
	return ctx, true
}

// writeBodyDecodeError maps a JSON body decode failure onto the lifecycle
// error contract: 413 when the MaxBytes cap tripped, 400 otherwise.
func writeBodyDecodeError(w http.ResponseWriter, err error) {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		httputil.JSON(w, http.StatusRequestEntityTooLarge, httputil.ErrorResponse{Error: "request body too large"})
		return
	}
	httputil.JSON(w, http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid request body: " + err.Error()})
}

// UpgradeHandler applies the migrations strictly between (req.From, req.To].
// Both fields must be migration numbers; semver must be translated by the
// platform before calling this endpoint (the platform reads the Versions map
// from the manifest and uses the scope-matching field of MigrationVersions).
//
// When the body also carries Schema + Credential (the platform's non-tunnel
// path), the migrations run under that per-(app, module) credential with
// search_path = schema — identical to InstallHandler. A bare {from, to}
// body keeps the dev-tunnel env-pool behavior.
func UpgradeHandler(sqlFS fs.FS, scope migration.Scope, runTx migration.TxRunner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := decodeUpgradeRequest(w, r)
		if !ok {
			return
		}
		ctx, ok := injectLifecycleContext(w, r.Context(), req.InstallRequest)
		if !ok {
			return
		}

		migrations, err := migration.List(sqlFS, scope)
		if err != nil {
			httputil.JSON(w, http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
			return
		}
		slice, err := migration.Slice(migrations, req.From, req.To)
		if err != nil {
			httputil.JSON(w, http.StatusBadRequest, httputil.ErrorResponse{Error: err.Error()})
			return
		}

		applied, err := migration.Apply(ctx, runTx, sqlFS, slice)
		if err != nil {
			httputil.JSON(w, http.StatusInternalServerError, LifecycleError{
				LifecycleResult: LifecycleResult{Applied: applied},
				Error:           err.Error(),
			})
			return
		}
		httputil.JSON(w, http.StatusOK, LifecycleResult{Applied: applied})
	}
}

// DowngradeHandler reverts migrations between (req.To, req.From] in newest-first
// order. Each migration must have a .down.sql or the request fails before any
// SQL runs. Both fields must be migration numbers (see UpgradeHandler).
// Schema + Credential in the body behave exactly as in UpgradeHandler.
func DowngradeHandler(sqlFS fs.FS, scope migration.Scope, runTx migration.TxRunner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := decodeUpgradeRequest(w, r)
		if !ok {
			return
		}
		ctx, ok := injectLifecycleContext(w, r.Context(), req.InstallRequest)
		if !ok {
			return
		}

		migrations, err := migration.List(sqlFS, scope)
		if err != nil {
			httputil.JSON(w, http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
			return
		}
		slice, err := migration.SliceDown(migrations, req.From, req.To)
		if err != nil {
			httputil.JSON(w, http.StatusBadRequest, httputil.ErrorResponse{Error: err.Error()})
			return
		}

		reverted, err := migration.ApplyDown(ctx, runTx, sqlFS, slice)
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

// decodeUpgradeRequest reads and validates the upgrade/downgrade body. From
// and To are required and must be numeric migration numbers (not semver) —
// the platform does semver resolution before calling. AppID, Schema and
// Credential are optional (see UpgradeRequest). Returns ok=false after
// writing a 400 response if the body is missing, malformed, or contains
// non-numeric version values.
//
// The numeric check here duplicates the parse inside migration.Slice, which
// is intentional: rejecting semver at the boundary gives us a tailored error
// message pointing at the platform-side translation step, and fast-fails
// before we bother reading the sql/ directory.
func decodeUpgradeRequest(w http.ResponseWriter, r *http.Request) (UpgradeRequest, bool) {
	var req UpgradeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBodyDecodeError(w, err)
		return req, false
	}
	if req.From == "" || req.To == "" {
		httputil.JSON(w, http.StatusBadRequest, httputil.ErrorResponse{Error: "from and to are required"})
		return req, false
	}
	if _, err := strconv.Atoi(req.From); err != nil {
		httputil.JSON(w, http.StatusBadRequest, httputil.ErrorResponse{Error: "from must be a migration number (did you forget to translate semver on the platform side?)"})
		return req, false
	}
	if _, err := strconv.Atoi(req.To); err != nil {
		httputil.JSON(w, http.StatusBadRequest, httputil.ErrorResponse{Error: "to must be a migration number (did you forget to translate semver on the platform side?)"})
		return req, false
	}
	return req, true
}
