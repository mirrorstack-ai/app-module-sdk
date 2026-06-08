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

func TestResolveEventBusURL_Building(t *testing.T) {
	cases := []struct {
		name     string
		dispatch string // value for MS_DISPATCH_URL ("" = unset -> dev fallback)
		appID    string
		event    string
		want     string
	}{
		{
			name:     "dev fallback when unset",
			dispatch: "",
			appID:    "a-456",
			event:    "user.created",
			want:     devDispatchFallback + "/apps/a-456/events/user.created",
		},
		{
			name:     "explicit base",
			dispatch: "http://dispatch:8083",
			appID:    "a-456",
			event:    "user.created",
			want:     "http://dispatch:8083/apps/a-456/events/user.created",
		},
		{
			name:     "trailing slash on base is trimmed",
			dispatch: "http://dispatch:8083/",
			appID:    "a-456",
			event:    "user.created",
			want:     "http://dispatch:8083/apps/a-456/events/user.created",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MS_DISPATCH_URL", tc.dispatch)
			if got := resolveEventBusURL(tc.appID, tc.event); got != tc.want {
				t.Errorf("resolveEventBusURL(%q, %q) = %q, want %q", tc.appID, tc.event, got, tc.want)
			}
		})
	}
}

func TestEmit_PostsEnvelopeFromContextAppID(t *testing.T) {
	var gotMethod, gotPath, gotAppID, gotCT string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAppID = r.Header.Get("X-MS-App-ID")
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"delivered":2}`))
	}))
	defer srv.Close()
	t.Setenv("MS_DISPATCH_URL", srv.URL)

	m, _ := New(Config{ID: "billing"})
	ctx := auth.Set(context.Background(), auth.Identity{AppID: "a-456"})

	if err := m.Emit(ctx, "payment.captured", map[string]any{"amount": 100}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	// appID is taken from ctx via auth.Set, encoded into both URL and header.
	if gotPath != "/apps/a-456/events/payment.captured" {
		t.Errorf("path = %q, want /apps/a-456/events/payment.captured", gotPath)
	}
	if gotAppID != "a-456" {
		t.Errorf("X-MS-App-ID = %q, want a-456", gotAppID)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotCT)
	}

	var env struct {
		ID             string         `json:"id"`
		Name           string         `json:"name"`
		SourceModuleID string         `json:"sourceModuleID"`
		SentAt         string         `json:"sentAt"`
		Payload        map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(gotBody, &env); err != nil {
		t.Fatalf("envelope unmarshal: %v (body=%s)", err, gotBody)
	}
	if env.ID == "" {
		t.Error("envelope id is empty, want a generated UUID")
	}
	if env.Name != "payment.captured" {
		t.Errorf("envelope name = %q, want payment.captured", env.Name)
	}
	if env.SourceModuleID != "billing" {
		t.Errorf("envelope sourceModuleID = %q, want billing (Config.ID)", env.SourceModuleID)
	}
	if env.SentAt == "" {
		t.Error("envelope sentAt is empty, want an RFC3339 timestamp")
	}
	if got := env.Payload["amount"]; got != float64(100) {
		t.Errorf("envelope payload.amount = %v, want 100", got)
	}
}

func TestEmit_EmptyAppContextErrorsWithoutHTTP(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	t.Setenv("MS_DISPATCH_URL", srv.URL)

	m, _ := New(Config{ID: "billing"})
	// No auth identity on the context -> no AppID.
	if err := m.Emit(context.Background(), "payment.captured", nil); err == nil {
		t.Fatal("expected error on empty-app context, got nil")
	}
	if hit {
		t.Error("Emit made an HTTP call despite empty app context")
	}
}

func TestEmit_Non2xxReturnsErrorWithTruncatedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("event bus unavailable"))
	}))
	defer srv.Close()
	t.Setenv("MS_DISPATCH_URL", srv.URL)

	m, _ := New(Config{ID: "billing"})
	ctx := auth.Set(context.Background(), auth.Identity{AppID: "a-456"})

	err := m.Emit(ctx, "payment.captured", nil)
	if err == nil {
		t.Fatal("expected error on non-2xx, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "502") {
		t.Errorf("error %q missing status 502", msg)
	}
	if !strings.Contains(msg, "event bus unavailable") {
		t.Errorf("error %q missing upstream body", msg)
	}
	if !strings.Contains(msg, "/apps/a-456/events/payment.captured") {
		t.Errorf("error %q missing request path", msg)
	}
}
