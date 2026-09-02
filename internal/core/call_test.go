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

func TestResolveDependencyCallURLUsesAppScopedRoute(t *testing.T) {
	t.Setenv("MS_DISPATCH_URL", "http://dispatch:8083")
	appID := "a722a8a8-d413-435b-b21b-f4cbacb5ef73"
	consumerID := "m14b4db3ac7f34e6a880ebc763cb3ca55"
	if got := resolveDependencyCallURL(appID, consumerID, "video-transcode", "/internal/jobs/start"); got != "http://dispatch:8083/internal/apps/"+appID+"/module-calls/"+consumerID+"/video-transcode/internal/jobs/start" {
		t.Fatalf("slug call URL = %q", got)
	}
}

func TestIsInternalCallPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		path string
		want bool
	}{
		{path: "/internal", want: true},
		{path: "/internal/jobs/start", want: true},
		{path: "/internal/jobs/start?retry=1", want: true},
		{path: "/public/jobs/start", want: false},
		{path: "/platform/jobs/start", want: false},
		{path: "/internalish/jobs/start", want: false},
		{path: "/internal/../platform/jobs", want: false},
		{path: "/internal/%2e%2e/platform/jobs", want: false},
		{path: "https://example.test/internal/jobs/start", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			if got := isInternalCallPath(tc.path); got != tc.want {
				t.Errorf("isInternalCallPath(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestCallDependency_UsesAuthenticatedInternalIngress(t *testing.T) {
	var gotMethod, gotPath, gotQuery, gotAppID, gotSecret string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAppID = r.Header.Get("X-MS-App-ID")
		gotSecret = r.Header.Get("X-MS-Service-Secret")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	t.Setenv("MS_DISPATCH_URL", srv.URL)
	t.Setenv("MS_INTERNAL_SECRET", "caller-session-secret")

	const appID = "a722a8a8-d413-435b-b21b-f4cbacb5ef73"
	const consumerID = "m14b4db3ac7f34e6a880ebc763cb3ca55"
	m, err := New(Config{ID: consumerID})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := auth.Set(context.Background(), auth.Identity{AppID: appID})
	if err := m.CallDependencyPost(ctx, "@mirrorstack/video-transcode@^0.1", "/internal/jobs/start?retry=1", map[string]string{"videoId": "v1"}, nil); err != nil {
		t.Fatalf("CallDependencyPost: %v", err)
	}

	wantPath := "/internal/apps/" + appID + "/module-calls/" + consumerID + "/video-transcode/internal/jobs/start"
	if gotMethod != http.MethodPost || gotPath != wantPath || gotQuery != "retry=1" {
		t.Fatalf("request = %s %s?%s, want POST %s?retry=1", gotMethod, gotPath, gotQuery, wantPath)
	}
	if gotAppID != appID {
		t.Errorf("X-MS-App-ID = %q, want %q", gotAppID, appID)
	}
	if gotSecret != "caller-session-secret" {
		t.Errorf("X-MS-Service-Secret = %q, want caller session secret", gotSecret)
	}
}

func TestCallDependency_RejectsNonInternalAndMissingApp(t *testing.T) {
	m, err := New(Config{ID: "consumer_id"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	withApp := auth.Set(context.Background(), auth.Identity{AppID: "app-id"})
	for _, path := range []string{"/public/jobs", "/platform/jobs", "/internal/../platform/jobs"} {
		if err := m.CallDependencyPost(withApp, "video-transcode", path, nil, nil); err == nil || !strings.Contains(err.Error(), "canonical /internal") {
			t.Errorf("CallDependencyPost(%q) error = %v, want internal-path rejection", path, err)
		}
	}
	if err := m.CallDependencyPost(context.Background(), "video-transcode", "/internal/jobs", nil, nil); err == nil || !strings.Contains(err.Error(), "no app scope") {
		t.Fatalf("missing-app error = %v", err)
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
