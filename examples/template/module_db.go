package main

// CLI flag: --use-module-db
// Remove this file if the module doesn't have cross-app state.
//
// ModuleDB / ModuleTx use the module's `mod_<id>` schema with credentials
// disjoint from the per-app schema. Use this for data that is global to the
// module (e.g. oauth state, pending tasks) not tied to a single app tenant.

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	ms "github.com/mirrorstack-ai/app-module-sdk"
	"github.com/mirrorstack-ai/app-module-sdk/db"
)

func init() {
	postInitHooks = append(postInitHooks, registerModuleDB)
}

func registerModuleDB() {
	ms.Internal(func(r chi.Router) {
		r.Post("/tasks/enqueue", enqueueTask)
	})
}

func enqueueTask(w http.ResponseWriter, r *http.Request) {
	err := ms.ModuleTx(r.Context(), func(q db.Querier) error {
		// Use q within the module schema. Example:
		//   _, err := q.Exec(r.Context(), "INSERT INTO pending_tasks (id) VALUES ($1)", id)
		_ = q
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}
