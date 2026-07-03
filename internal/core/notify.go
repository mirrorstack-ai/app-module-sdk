package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mirrorstack-ai/app-module-sdk/internal/ids"
)

// NotifyAudience selects WHO inside the app receives a notification. The
// value travels on the wire (the envelope's "audience" field), so the strings
// are a SHARED CONTRACT with the dispatch's audience whitelist.
type NotifyAudience string

const (
	// NotifyAdmins targets the app's admins only — the default when a
	// Notification leaves Audience unset.
	NotifyAdmins NotifyAudience = "admins"
	// NotifyAllMembers targets every member of the app.
	NotifyAllMembers NotifyAudience = "members"
)

// Notification is the module-facing shape passed to Notify. Title and Body
// are i18n Labels (Text literal or T catalog key) resolved to per-locale maps
// AT SEND TIME, so the platform — not the module — picks the recipient's
// locale. Title is required; Body, Icon and Link are optional. An unset
// Audience defaults to NotifyAdmins.
type Notification struct {
	Title    Label
	Body     Label
	Icon     string
	Link     string
	Audience NotifyAudience
}

// notifyEnvelope is the wire shape every notification takes. The dispatch's
// POST /apps/{appID}/notifications route decodes this struct, so the JSON
// field names here are a SHARED CONTRACT with the dispatch — don't rename
// them without changing both sides.
type notifyEnvelope struct {
	ID             string            `json:"id"`             // per-send UUID (idempotency key)
	SentAt         string            `json:"sentAt"`         // RFC3339 UTC timestamp of the send
	SourceModuleID string            `json:"sourceModuleID"` // sending module's Config.ID
	Title          map[string]string `json:"title"`          // locale -> title (Label.Resolve)
	Body           map[string]string `json:"body"`           // locale -> body; empty map when Body is unset
	Icon           string            `json:"icon"`           // optional icon name
	Link           string            `json:"link"`           // optional in-app link target
	Audience       NotifyAudience    `json:"audience"`       // "admins" | "members"
}

// resolveNotifyURL builds the platform-dispatch URL the envelope is POSTed to:
//
//	{base}/apps/{appID}/notifications
//
// DEV/DISPATCH TRANSPORT. Prod notification-ingress resolution is task #146 —
// this resolver (path) and dispatchBase (base) are the seams where that plugs
// in, mirroring resolveEventBusURL. Notify's marshal/auth/error contract
// (dispatch_transport.go) stays put.
func resolveNotifyURL(appID string) string {
	return fmt.Sprintf("%s/apps/%s/notifications", dispatchBase(), appID)
}

// Notify sends an in-app notification to the current app's members. It
// resolves the Notification's i18n Labels to per-locale maps, wraps them in
// the notification envelope (a fresh UUID id, the sending module's Config.ID
// as sourceModuleID, and an RFC3339 UTC sentAt), then POSTs the envelope to
// the platform dispatch notification ingress scoped to the current app. The
// dispatch re-derives the sender identity from the live session — the
// envelope's sourceModuleID is informational, never trusted.
//
// The app id is read from the request context (auth.Get) — the same identity
// the SDK injects for handlers — and is encoded in the dispatch URL. An empty
// app id is an error (no panic): Notify needs an app scope to know whose feed
// receives the notification.
//
// Title is REQUIRED — a Title that resolves to no message (unset, or an empty
// literal) is an error (no panic). Body is optional (an unset Body sends an
// empty map). An unset Audience defaults to
// NotifyAdmins; any value other than NotifyAdmins/NotifyAllMembers is an
// error. A non-2xx response from the dispatch is returned as an error with
// the response body truncated to ~2 KB.
//
// DEV/DISPATCH TRANSPORT — see resolveNotifyURL for the prod (#146) seam.
func (m *Module) Notify(ctx context.Context, n Notification) error {
	appID, err := appIDFromContext(ctx, "Notify")
	if err != nil {
		return err
	}

	title := n.Title.Resolve()
	if !hasMessage(title) {
		return errors.New("mirrorstack: Notify requires a Title (ms.Text or ms.T)")
	}

	audience := n.Audience
	if audience == "" {
		audience = NotifyAdmins
	}
	if audience != NotifyAdmins && audience != NotifyAllMembers {
		return fmt.Errorf("mirrorstack: Notify audience %q is not %q or %q", audience, NotifyAdmins, NotifyAllMembers)
	}

	body := map[string]string{}
	if !n.Body.IsZero() {
		body = n.Body.Resolve()
	}
	env := notifyEnvelope{
		ID:             ids.NewUUID(),
		SentAt:         time.Now().UTC().Format(time.RFC3339Nano),
		SourceModuleID: m.config.ID,
		Title:          title,
		Body:           body,
		Icon:           n.Icon,
		Link:           n.Link,
		Audience:       audience,
	}
	return postDispatchJSON(ctx, "ms.Notify", resolveNotifyURL(appID), appID, env)
}

// hasMessage reports whether a resolved locale map carries at least one
// non-empty message. An unset Label resolves to {DefaultLocale: ""}, so a map
// with only empty values means the caller never gave the field a value.
// (Deliberately stricter than Label.IsZero, which admits ms.Text("").)
func hasMessage(resolved map[string]string) bool {
	for _, msg := range resolved {
		if msg != "" {
			return true
		}
	}
	return false
}

// Notify sends a notification on the default module created by Init(). Panics
// before Init — matches Call/CallGet/CallPost/Emit.
func Notify(ctx context.Context, n Notification) error {
	return mustDefault("Notify").Notify(ctx, n)
}
