package contributions

import (
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/httputil"
)

// MaxPayloadBytes caps the body size for register requests. Slot
// payloads are config metadata (icon, label, login_path) — small. A
// 16 KB cap keeps a misbehaving contributor from blowing up the
// host's DB row size while leaving headroom for moderately rich
// shapes (e.g. arrays of column definitions for users-table-columns).
const MaxPayloadBytes = 16 * 1024

// DBFunc supplies a database connection scoped to the request. The
// SDK already exposes ms.DB; the handlers take it as an interface so
// tests can inject a fake.
type DBFunc func(r *http.Request) (db.Querier, func(), error)

// Handlers wires the registry + storage + DB accessor into chi
// handlers the SDK can mount under /__mirrorstack/contrib.
type Handlers struct {
	registry *Registry
	storage  *Storage
	openDB   DBFunc
}

// NewHandlers constructs a handler set.
func NewHandlers(registry *Registry, storage *Storage, openDB DBFunc) *Handlers {
	return &Handlers{registry: registry, storage: storage, openDB: openDB}
}

// Routes returns a chi router scoped under /__mirrorstack/contrib.
// Caller is responsible for applying auth middleware (Internal scope
// is the expected wrapper — contributions move trusted module-to-
// module so the secret gate guards the write path).
func (h *Handlers) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/{slot}/{id}", h.register)
	r.Delete("/{slot}/{id}", h.unregister)
	r.Get("/{slot}", h.list)
	return r
}

// register handles POST /{slot}/{id}. Body is the typed payload.
// Validated against the slot's declared T (unmarshal probe) before
// hitting storage.
func (h *Handlers) register(w http.ResponseWriter, r *http.Request) {
	slotKey := chi.URLParam(r, "slot")
	id := chi.URLParam(r, "id")

	slot, ok := h.registry.Get(slotKey)
	if !ok {
		httputil.JSON(w, http.StatusNotFound, httputil.ErrorResponse{Error: "unknown contribution slot"})
		return
	}
	if id == "" {
		httputil.JSON(w, http.StatusBadRequest, httputil.ErrorResponse{Error: "contribution id is required"})
		return
	}

	// Outer httputil.MaxBytes middleware (mounted by core.Module on
	// the /__mirrorstack/contrib subtree) already caps the body —
	// just read it.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		httputil.JSON(w, http.StatusBadRequest, httputil.ErrorResponse{Error: "payload unreadable"})
		return
	}
	if err := slot.Validate(body); err != nil {
		httputil.JSON(w, http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid payload: " + err.Error()})
		return
	}

	q, release, err := h.openDB(r)
	if err != nil {
		httputil.JSON(w, http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
		return
	}
	defer release()

	if err := h.storage.Upsert(r.Context(), q, slotKey, id, body); err != nil {
		httputil.JSON(w, http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
		return
	}
	httputil.JSON(w, http.StatusOK, map[string]string{"id": id, "slot": slotKey})
}

// unregister handles DELETE /{slot}/{id}. Returns 204 on success,
// 404 if the contribution wasn't registered.
func (h *Handlers) unregister(w http.ResponseWriter, r *http.Request) {
	slotKey := chi.URLParam(r, "slot")
	id := chi.URLParam(r, "id")

	if _, ok := h.registry.Get(slotKey); !ok {
		httputil.JSON(w, http.StatusNotFound, httputil.ErrorResponse{Error: "unknown contribution slot"})
		return
	}

	q, release, err := h.openDB(r)
	if err != nil {
		httputil.JSON(w, http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
		return
	}
	defer release()

	if err := h.storage.Delete(r.Context(), q, slotKey, id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.JSON(w, http.StatusNotFound, httputil.ErrorResponse{Error: "contribution not found"})
			return
		}
		httputil.JSON(w, http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// list handles GET /{slot}. Returns the registered contributions
// sorted newest-first.
func (h *Handlers) list(w http.ResponseWriter, r *http.Request) {
	slotKey := chi.URLParam(r, "slot")

	if _, ok := h.registry.Get(slotKey); !ok {
		httputil.JSON(w, http.StatusNotFound, httputil.ErrorResponse{Error: "unknown contribution slot"})
		return
	}

	q, release, err := h.openDB(r)
	if err != nil {
		httputil.JSON(w, http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
		return
	}
	defer release()

	out, err := h.storage.List(r.Context(), q, slotKey)
	if err != nil {
		httputil.JSON(w, http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
		return
	}
	httputil.JSON(w, http.StatusOK, map[string]any{"contributions": out})
}
