package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
)

func TestResolveCallURL_Building(t *testing.T) {
	cases := []struct {
		name     string
		dispatch string // value for MS_DISPATCH_URL ("" = unset -> dev fallback)
		target   string
		path     string
		want     string
	}{
		{
			name:     "dev fallback when unset",
			dispatch: "",
			target:   "m0123",
			path:     "/internal/exchange",
			want:     devDispatchFallback + "/module/m0123/internal/exchange",
		},
		{
			name:     "explicit base",
			dispatch: "http://dispatch:8083",
			target:   "m0123",
			path:     "/internal/users",
			want:     "http://dispatch:8083/module/m0123/internal/users",
		},
		{
			name:     "trailing slash on base is trimmed",
			dispatch: "http://dispatch:8083/",
			target:   "m0123",
			path:     "/internal/users",
			want:     "http://dispatch:8083/module/m0123/internal/users",
		},
		{
			name:     "raw query carried in path",
			dispatch: "http://dispatch:8083",
			target:   "m0123",
			path:     "/internal/users?limit=10",
			want:     "http://dispatch:8083/module/m0123/internal/users?limit=10",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.dispatch == "" {
				t.Setenv("MS_DISPATCH_URL", "")
			} else {
				t.Setenv("MS_DISPATCH_URL", tc.dispatch)
			}
			if got := resolveCallURL(tc.target, tc.path); got != tc.want {
				t.Errorf("resolveCallURL(%q, %q) = %q, want %q", tc.target, tc.path, got, tc.want)
			}
		})
	}
}

func TestCall_SendsAppIDAndDecodesResponse(t *testing.T) {
	var gotMethod, gotPath, gotAppID, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAppID = r.Header.Get("X-MS-App-ID")
		gotCT = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("MS_DISPATCH_URL", srv.URL)

	m, _ := New(Config{ID: "demo"})
	ctx := auth.Set(context.Background(), auth.Identity{AppID: "a-456"})

	var out struct {
		OK bool `json:"ok"`
	}
	if err := m.CallPost(ctx, "m0123", "/internal/exchange", map[string]string{"code": "x"}, &out); err != nil {
		t.Fatalf("CallPost: %v", err)
	}
	if !out.OK {
		t.Errorf("out.OK = false, want true")
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/module/m0123/internal/exchange" {
		t.Errorf("path = %q, want /module/m0123/internal/exchange", gotPath)
	}
	if gotAppID != "a-456" {
		t.Errorf("X-MS-App-ID = %q, want a-456", gotAppID)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotCT)
	}
}

func TestCall_Non2xxReturnsErrorWithTruncatedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream module unavailable"))
	}))
	defer srv.Close()
	t.Setenv("MS_DISPATCH_URL", srv.URL)

	m, _ := New(Config{ID: "demo"})
	err := m.CallGet(context.Background(), "m0123", "/internal/users", nil)
	if err == nil {
		t.Fatal("expected error on non-2xx, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "502") {
		t.Errorf("error %q missing status 502", msg)
	}
	if !strings.Contains(msg, "upstream module unavailable") {
		t.Errorf("error %q missing upstream body", msg)
	}
	if !strings.Contains(msg, "/module/m0123/internal/users") {
		t.Errorf("error %q missing request path", msg)
	}
}

func TestCallGet_NoBodyNoContentType(t *testing.T) {
	var hadCT bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hadCT = r.Header.Get("Content-Type") != ""
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	t.Setenv("MS_DISPATCH_URL", srv.URL)

	m, _ := New(Config{ID: "demo"})
	if err := m.CallGet(context.Background(), "m0123", "/internal/ping", nil); err != nil {
		t.Fatalf("CallGet: %v", err)
	}
	if hadCT {
		t.Error("GET set a Content-Type header, want none (no body)")
	}
}
