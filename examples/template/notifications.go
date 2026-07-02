package main

// CLI flag: --use-notifications
// Remove this file if the module doesn't send in-app notifications.
//
// ms.Notify writes a notification into the current app's feed. Title/Body are
// i18n Labels (ms.Text literal or ms.T catalog key, backed by ms.RegisterMessages)
// resolved to per-locale maps AT SEND TIME, so the platform picks each
// recipient's locale — the module never picks one. Audience targets the app's
// admins (the default) or every member; the platform re-derives the sender
// module from the live session, so the envelope identity is never trusted.

import (
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	ms "github.com/mirrorstack-ai/app-module-sdk"
)

func init() {
	postInitHooks = append(postInitHooks, registerNotifications)
}

func registerNotifications() {
	ms.Platform(func(r chi.Router) {
		r.Post("/reports", func(w http.ResponseWriter, r *http.Request) {
			// ... generate the report ...

			// Notify the app's members. The ms.T keys resolve against the
			// catalogs loaded via ms.RegisterMessages (i18n/<locale>.json),
			// e.g. {"notifications":{"report":{"ready":"Report ready"}}}.
			// A notification failure must not fail the handler — log it.
			if err := ms.Notify(r.Context(), ms.Notification{
				Title:    ms.T("notifications.report.ready"),
				Body:     ms.T("notifications.report.ready_body"),
				Icon:     "description",
				Link:     "/reports/latest",
				Audience: ms.NotifyAllMembers, // omit to target admins only
			}); err != nil {
				log.Printf("notify: %v", err) // don't fail the handler
			}
			w.WriteHeader(http.StatusOK)
		})
	})
}
