package core

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
)

func TestResolveNotifyURL_Building(t *testing.T) {
	cases := []struct {
		name     string
		dispatch string // value for MS_DISPATCH_URL ("" = unset -> dev fallback)
		appID    string
		want     string
	}{
		{
			name:     "dev fallback when unset",
			dispatch: "",
			appID:    "a-456",
			want:     devDispatchFallback + "/apps/a-456/notifications",
		},
		{
			name:     "explicit base",
			dispatch: "http://dispatch:8083",
			appID:    "a-456",
			want:     "http://dispatch:8083/apps/a-456/notifications",
		},
		{
			name:     "trailing slash on base is trimmed",
			dispatch: "http://dispatch:8083/",
			appID:    "a-456",
			want:     "http://dispatch:8083/apps/a-456/notifications",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MS_DISPATCH_URL", tc.dispatch)
			if got := resolveNotifyURL(tc.appID); got != tc.want {
				t.Errorf("resolveNotifyURL(%q) = %q, want %q", tc.appID, got, tc.want)
			}
		})
	}
}

// TestNotify_EnvelopeContract pins the wire shape: the JSON field names are a
// SHARED CONTRACT with the dispatch's POST /apps/{appID}/notifications route.
func TestNotify_EnvelopeContract(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	t.Setenv("MS_DISPATCH_URL", srv.URL)

	m, _ := New(Config{ID: "billing"})
	ctx := auth.Set(context.Background(), auth.Identity{AppID: "a-456"})

	err := m.Notify(ctx, Notification{
		Title:    Text("Order placed"),
		Body:     Text("Order #42 was placed."),
		Icon:     "shopping_cart",
		Link:     "/orders/42",
		Audience: NotifyAllMembers,
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}

	// Exact field-name check: unmarshal into a raw map so a renamed or
	// missing key fails loudly instead of zero-valuing.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(gotBody, &raw); err != nil {
		t.Fatalf("envelope unmarshal: %v (body=%s)", err, gotBody)
	}
	for _, key := range []string{"id", "sentAt", "sourceModuleID", "title", "body", "icon", "link", "audience"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("envelope missing field %q (body=%s)", key, gotBody)
		}
	}
	if len(raw) != 8 {
		t.Errorf("envelope has %d fields, want 8 (body=%s)", len(raw), gotBody)
	}

	var env struct {
		ID             string            `json:"id"`
		SentAt         string            `json:"sentAt"`
		SourceModuleID string            `json:"sourceModuleID"`
		Title          map[string]string `json:"title"`
		Body           map[string]string `json:"body"`
		Icon           string            `json:"icon"`
		Link           string            `json:"link"`
		Audience       string            `json:"audience"`
	}
	if err := json.Unmarshal(gotBody, &env); err != nil {
		t.Fatalf("envelope unmarshal: %v (body=%s)", err, gotBody)
	}
	if env.ID == "" {
		t.Error("envelope id is empty, want a generated UUID")
	}
	if env.SentAt == "" {
		t.Error("envelope sentAt is empty, want an RFC3339 timestamp")
	}
	if env.SourceModuleID != "billing" {
		t.Errorf("envelope sourceModuleID = %q, want billing (Config.ID)", env.SourceModuleID)
	}
	// Text() literals resolve under the default locale.
	if got := env.Title["en-US"]; got != "Order placed" {
		t.Errorf("envelope title[en-US] = %q, want Order placed", got)
	}
	if got := env.Body["en-US"]; got != "Order #42 was placed." {
		t.Errorf("envelope body[en-US] = %q, want Order #42 was placed.", got)
	}
	if env.Icon != "shopping_cart" {
		t.Errorf("envelope icon = %q, want shopping_cart", env.Icon)
	}
	if env.Link != "/orders/42" {
		t.Errorf("envelope link = %q, want /orders/42", env.Link)
	}
	if env.Audience != "members" {
		t.Errorf("envelope audience = %q, want members", env.Audience)
	}
}

func TestNotify_PostsFromContextAppID(t *testing.T) {
	var gotMethod, gotPath, gotAppID, gotCT string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAppID = r.Header.Get("X-MS-App-ID")
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	t.Setenv("MS_DISPATCH_URL", srv.URL)

	m, _ := New(Config{ID: "billing"})
	ctx := auth.Set(context.Background(), auth.Identity{AppID: "a-456"})

	if err := m.Notify(ctx, Notification{Title: Text("Hello")}); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	// appID is taken from ctx via auth.Set, encoded into both URL and header.
	if gotPath != "/apps/a-456/notifications" {
		t.Errorf("path = %q, want /apps/a-456/notifications", gotPath)
	}
	if gotAppID != "a-456" {
		t.Errorf("X-MS-App-ID = %q, want a-456", gotAppID)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotCT)
	}

	var env struct {
		Body     map[string]string `json:"body"`
		Audience string            `json:"audience"`
	}
	if err := json.Unmarshal(gotBody, &env); err != nil {
		t.Fatalf("envelope unmarshal: %v (body=%s)", err, gotBody)
	}
	// Defaults: unset Audience -> admins; unset Body -> empty map (not null).
	if env.Audience != "admins" {
		t.Errorf("envelope audience = %q, want admins (default)", env.Audience)
	}
	if env.Body == nil || len(env.Body) != 0 {
		t.Errorf("envelope body = %v, want empty map for unset Body", env.Body)
	}
}

func TestNotify_ValidationErrorsWithoutHTTP(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	t.Setenv("MS_DISPATCH_URL", srv.URL)

	m, _ := New(Config{ID: "billing"})
	appCtx := auth.Set(context.Background(), auth.Identity{AppID: "a-456"})

	cases := []struct {
		name string
		ctx  context.Context
		n    Notification
	}{
		{
			// No auth identity on the context -> no AppID.
			name: "empty app context",
			ctx:  context.Background(),
			n:    Notification{Title: Text("Hello")},
		},
		{
			name: "unset Title",
			ctx:  appCtx,
			n:    Notification{Body: Text("body only")},
		},
		{
			name: "empty-literal Title",
			ctx:  appCtx,
			n:    Notification{Title: Text("")},
		},
		{
			name: "unknown audience",
			ctx:  appCtx,
			n:    Notification{Title: Text("Hello"), Audience: NotifyAudience("everyone")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := m.Notify(tc.ctx, tc.n); err == nil {
				t.Fatal("expected error, got nil")
			}
			if hit {
				t.Error("Notify made an HTTP call despite failing validation")
			}
		})
	}
}

func TestNotify_Non2xxReturnsErrorWithTruncatedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("notification ingress unavailable"))
	}))
	defer srv.Close()
	t.Setenv("MS_DISPATCH_URL", srv.URL)

	m, _ := New(Config{ID: "billing"})
	ctx := auth.Set(context.Background(), auth.Identity{AppID: "a-456"})

	err := m.Notify(ctx, Notification{Title: Text("Hello")})
	if err == nil {
		t.Fatal("expected error on non-2xx, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "502") {
		t.Errorf("error %q missing status 502", msg)
	}
	if !strings.Contains(msg, "notification ingress unavailable") {
		t.Errorf("error %q missing upstream body", msg)
	}
	if !strings.Contains(msg, "/apps/a-456/notifications") {
		t.Errorf("error %q missing request path", msg)
	}
}

func TestNotify_Non2xxBodyIsTruncatedTo2KB(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(strings.Repeat("x", 10_000)))
	}))
	defer srv.Close()
	t.Setenv("MS_DISPATCH_URL", srv.URL)

	m, _ := New(Config{ID: "billing"})
	ctx := auth.Set(context.Background(), auth.Identity{AppID: "a-456"})

	err := m.Notify(ctx, Notification{Title: Text("Hello")})
	if err == nil {
		t.Fatal("expected error on non-2xx, got nil")
	}
	if got := len(err.Error()); got > 2048+128 { // 2KB body + message framing
		t.Errorf("error message is %d bytes, want upstream body truncated to ~2KB", got)
	}
}
