package event

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/mirrorstack-ai/app-module-sdk/handler"
)

// Register mounts event subscriber routes under /events/ on the router.
// Routes are protected by RequireInternal — only the platform can deliver events.
//
//	event.Register(r, map[string]event.HandlerFunc{
//	    "oauth.user_created": h.OnUserCreated,
//	    "oauth.user_deleted": h.OnUserDeleted,
//	})
//
// Creates routes:
//
//	POST /events/oauth.user_created
//	POST /events/oauth.user_deleted
func Register(r chi.Router, handlers map[string]HandlerFunc) {
	r.Route("/events", func(r chi.Router) {
		r.Use(handler.RequireInternal)
		for eventType, fn := range handlers {
			r.Post("/"+eventType, newEventHandler(fn))
		}
	})
}

func newEventHandler(fn HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var evt Event
		if err := handler.DecodeJSON(w, r, &evt); err != nil {
			return
		}
		fn(w, r, evt)
	}
}
