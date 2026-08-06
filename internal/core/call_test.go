package core

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
)

func TestDispatchBaseFallsBackWhenUnset(t *testing.T) {
	t.Setenv("MS_DISPATCH_URL", "")

	if got := dispatchBase(); got != devDispatchFallback {
		t.Errorf("dispatchBase() = %q, want %q", got, devDispatchFallback)
	}
}

func TestDispatchBaseTrimsTrailingSlash(t *testing.T) {
	t.Setenv("MS_DISPATCH_URL", "https://api.mirrorstack.ai/dispatch/")

	const want = "https://api.mirrorstack.ai/dispatch"
	if got := dispatchBase(); got != want {
		t.Errorf("dispatchBase() = %q, want %q", got, want)
	}
}

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

func TestCall_SendsAppIDServiceSecretAndDecodesResponse(t *testing.T) {
	var gotMethod, gotPath, gotAppID, gotCT string
	var gotSecrets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAppID = r.Header.Get("X-MS-App-ID")
		gotCT = r.Header.Get("Content-Type")
		gotSecrets = r.Header.Values("X-MS-Service-Secret")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("MS_DISPATCH_URL", srv.URL)
	t.Setenv("MS_INTERNAL_SECRET", "session-secret-1")

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
	if len(gotSecrets) != 1 || gotSecrets[0] != "session-secret-1" {
		t.Errorf("X-MS-Service-Secret values = %q, want exactly [session-secret-1]", gotSecrets)
	}
}

func TestCall_OmitsEmptyServiceSecret(t *testing.T) {
	var gotSecrets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSecrets = r.Header.Values("X-MS-Service-Secret")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	t.Setenv("MS_DISPATCH_URL", srv.URL)
	t.Setenv("MS_INTERNAL_SECRET", "")

	m, _ := New(Config{ID: "demo"})
	ctx := auth.Set(context.Background(), auth.Identity{AppID: "a-456"})
	if err := m.CallGet(ctx, "m0123", "/internal/ping", nil); err != nil {
		t.Fatalf("CallGet: %v", err)
	}
	if len(gotSecrets) != 0 {
		t.Errorf("X-MS-Service-Secret values = %q, want no header", gotSecrets)
	}
}

func TestCall_Non2xxReturnsErrorWithTruncatedBody(t *testing.T) {
	const serviceSecret = "session-secret-must-not-leak"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream module unavailable"))
	}))
	defer srv.Close()
	t.Setenv("MS_DISPATCH_URL", srv.URL)
	t.Setenv("MS_INTERNAL_SECRET", serviceSecret)

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
	if strings.Contains(msg, serviceSecret) {
		t.Errorf("error leaked X-MS-Service-Secret: %q", msg)
	}
}

func TestCall_TransportErrorIncludesResolvedHost(t *testing.T) {
	const dispatchURL = "http://unreachable-dispatch.example:8083"
	const serviceSecret = "session-secret-must-not-leak"
	t.Setenv("MS_DISPATCH_URL", dispatchURL)
	t.Setenv("MS_INTERNAL_SECRET", serviceSecret)

	previousClient := callHTTP
	callHTTP = &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection refused")
		}),
	}
	t.Cleanup(func() { callHTTP = previousClient })

	m, _ := New(Config{ID: "demo"})
	err := m.CallGet(context.Background(), "m0123", "/internal/users", nil)
	if err == nil {
		t.Fatal("expected transport error, got nil")
	}
	if !strings.Contains(err.Error(), dispatchURL) {
		t.Errorf("error %q missing resolved dispatch host %q", err, dispatchURL)
	}
	if strings.Contains(err.Error(), serviceSecret) {
		t.Errorf("error leaked X-MS-Service-Secret: %q", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
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
