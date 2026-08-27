package core

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/internal/actor"
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

func TestResolveDependencyCallURL_Building(t *testing.T) {
	t.Setenv("MS_DISPATCH_URL", "http://dispatch:8083")
	got := resolveDependencyCallURL("app slug", "consumer/ref", "video-transcode", "/internal/jobs/start?retry=1")
	want := "http://dispatch:8083/internal/apps/app%20slug/module-calls/consumer%2Fref/video-transcode/internal/jobs/start?retry=1"
	if got != want {
		t.Fatalf("resolveDependencyCallURL() = %q, want %q", got, want)
	}
}

func TestIsPlatformCallPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		path string
		want bool
	}{
		{path: "/platform", want: true},
		{path: "/platform/", want: true},
		{path: "/platform/users", want: true},
		{path: "/platform/users?limit=10", want: true},
		{path: "/public/users", want: false},
		{path: "/internal/users", want: false},
		{path: "/platformish/users", want: false},
		{path: "/platform/../internal/users", want: false},
		{path: "/platform/%2e%2e/internal/users", want: false},
		{path: "https://example.test/platform/users", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			if got := isPlatformCallPath(tc.path); got != tc.want {
				t.Errorf("isPlatformCallPath(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestIsInternalCallPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		path string
		want bool
	}{
		{path: "/internal", want: true},
		{path: "/internal/", want: true},
		{path: "/internal/jobs?retry=1", want: true},
		{path: "/public/jobs", want: false},
		{path: "/platform/jobs", want: false},
		{path: "/internalish/jobs", want: false},
		{path: "/internal/../platform/jobs", want: false},
		{path: "/internal/%2e%2e/platform/jobs", want: false},
		{path: "/internal/jobs%2fadmin", want: false},
		{path: "/internal/jobs%5cescape", want: false},
		{path: "/internal/jobs\\escape", want: false},
		{path: "https://example.test/internal/jobs", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			if got := isInternalCallPath(tc.path); got != tc.want {
				t.Errorf("isInternalCallPath(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestCallDependency_UsesAuthenticatedActorlessInternalIngress(t *testing.T) {
	var gotMethod, gotPath, gotQuery, gotAppID, gotSecret string
	var gotDelegation []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAppID = r.Header.Get("X-MS-App-ID")
		gotSecret = r.Header.Get("X-MS-Service-Secret")
		gotDelegation = r.Header.Values(actor.HeaderDelegation)
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
	ctx = actor.WithDelegation(ctx, "msa1.must-not-cross.signature")
	if err := m.CallDependencyPost(ctx, "@mirrorstack/video-transcode@^0.1", "/internal/jobs/start?retry=1", map[string]string{"videoId": "v1"}, nil); err != nil {
		t.Fatalf("CallDependencyPost: %v", err)
	}

	wantPath := "/internal/apps/" + appID + "/module-calls/" + consumerID + "/video-transcode/internal/jobs/start"
	if gotMethod != http.MethodPost || gotPath != wantPath || gotQuery != "retry=1" {
		t.Fatalf("request = %s %s?%s, want POST %s?retry=1", gotMethod, gotPath, gotQuery, wantPath)
	}
	if gotAppID != appID || gotSecret != "caller-session-secret" {
		t.Errorf("identity headers app=%q secret=%q", gotAppID, gotSecret)
	}
	if len(gotDelegation) != 0 {
		t.Errorf("actor delegation leaked to dependency: %q", gotDelegation)
	}
}

func TestCallDependency_DevSecureIngressWins(t *testing.T) {
	var gotPath string
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	t.Setenv("MS_DISPATCH_URL", srv.URL)
	t.Setenv("MS_INTERNAL_SECRET", "caller-session-secret")

	const appID = "a722a8a8-d413-435b-b21b-f4cbacb5ef73"
	const consumerID = "m14b4db3ac7f34e6a880ebc763cb3ca55"
	m, err := New(Config{ID: consumerID, Slug: "consumer"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m.devMode = true
	m.DependsOn("video-transcode")
	lookups := 0
	m.devDir.lookup = func(context.Context, string) (devModuleEntry, bool, error) {
		lookups++
		return devModuleEntry{ModuleID: "m5ae9627818b74bd1ae23031455822070", Slug: "video-transcode"}, true, nil
	}

	ctx := auth.Set(context.Background(), auth.Identity{AppID: appID})
	if err := m.CallDependencyPost(ctx, "video-transcode", "/internal/jobs", nil, nil); err != nil {
		t.Fatalf("CallDependencyPost: %v", err)
	}
	want := "/internal/apps/" + appID + "/module-calls/" + consumerID + "/video-transcode/internal/jobs"
	if requests != 1 || gotPath != want {
		t.Fatalf("secure ingress requests=%d path=%q, want one request to %q", requests, gotPath, want)
	}
	if lookups != 0 {
		t.Fatalf("secure ingress success consulted dev directory %d time(s)", lookups)
	}
}

func TestCallDependency_Dev404UsesDeclaredColocatedDirectRoute(t *testing.T) {
	var gotMethod, gotPath, gotQuery, gotAppID, gotSecret, gotPlatformToken, gotProducer string
	var gotDelegation []string
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if strings.HasPrefix(r.URL.Path, "/internal/apps/") {
			http.NotFound(w, r)
			return
		}
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAppID = r.Header.Get("X-MS-App-ID")
		gotSecret = r.Header.Get("X-MS-Service-Secret")
		gotPlatformToken = r.Header.Get(auth.HeaderPlatformToken)
		gotDelegation = r.Header.Values(actor.HeaderDelegation)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	t.Setenv("MS_DISPATCH_URL", srv.URL)
	t.Setenv("MS_LOCAL_MODULE_PROXY_URL", srv.URL)
	t.Setenv("MS_INTERNAL_SECRET", "caller-session-secret")
	tokenDir := t.TempDir()
	callerTokenFile := filepath.Join(tokenDir, "ms-platform-token-consumer")
	if err := os.WriteFile(callerTokenFile, []byte("caller-platform-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tokenDir, "ms-platform-token-video-transcode"), []byte("producer-platform-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MS_PLATFORM_TOKEN_FILE", callerTokenFile)

	const appID = "a722a8a8-d413-435b-b21b-f4cbacb5ef73"
	const producerID = "m5ae9627818b74bd1ae23031455822070"
	m, err := New(Config{ID: "m14b4db3ac7f34e6a880ebc763cb3ca55", Slug: "consumer"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m.devMode = true
	m.DependsOn("@mirrorstack/video-transcode@^0.1")
	m.devDir.lookup = func(_ context.Context, ref string) (devModuleEntry, bool, error) {
		gotProducer = ref
		return devModuleEntry{ModuleID: producerID, Slug: "video-transcode"}, true, nil
	}

	ctx := auth.Set(context.Background(), auth.Identity{AppID: appID})
	ctx = actor.WithDelegation(ctx, "msa1.must-not-cross.signature")
	if err := m.CallDependencyPost(ctx, "@mirrorstack/video-transcode@^0.2", "/internal/jobs/start?retry=1", map[string]string{"videoId": "v1"}, nil); err != nil {
		t.Fatalf("CallDependencyPost: %v", err)
	}

	if gotProducer != "video-transcode" {
		t.Fatalf("directory lookup ref = %q, want video-transcode", gotProducer)
	}
	wantPath := "/_m/video-transcode/internal/jobs/start"
	if gotMethod != http.MethodPost || gotPath != wantPath || gotQuery != "retry=1" {
		t.Fatalf("request = %s %s?%s, want POST %s?retry=1", gotMethod, gotPath, gotQuery, wantPath)
	}
	if gotAppID != appID || gotSecret != "" || gotPlatformToken != "producer-platform-token" {
		t.Errorf("identity headers app=%q service_secret=%q platform_token=%q", gotAppID, gotSecret, gotPlatformToken)
	}
	if len(gotDelegation) != 0 {
		t.Errorf("actor delegation leaked to direct dependency: %q", gotDelegation)
	}

	before := len(paths)
	oversized := map[string]string{"value": strings.Repeat("x", maxDependencyCallBody)}
	if err := m.CallDependencyPost(ctx, "video-transcode", "/internal/jobs", oversized, nil); err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("oversized direct-call error = %v", err)
	}
	if len(paths) != before {
		t.Fatalf("oversized direct call reached network: requests %d -> %d", before, len(paths))
	}
	wantSecurePath := "/internal/apps/" + appID + "/module-calls/m14b4db3ac7f34e6a880ebc763cb3ca55/video-transcode/internal/jobs/start"
	if len(paths) != 2 || paths[0] != wantSecurePath || paths[1] != wantPath {
		t.Fatalf("fallback requests = %q, want [%q %q]", paths, wantSecurePath, wantPath)
	}
}

func TestCallDependency_Dev404FromIngressOrProducerNeverFallsBack(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		routed      bool
		routedValue string
	}{
		{
			name:        "module not installed structured 404",
			contentType: "application/json",
			body:        `{"error":{"code":"module_not_found","message":"dependency module is not installed"}}`,
		},
		{
			name:        "forwarded producer default-looking 404",
			contentType: legacyRouteMissContentType,
			body:        legacyRouteMissBody,
			routed:      true,
			routedValue: "1",
		},
		{
			name:        "even an empty routed marker proves ingress ownership",
			contentType: legacyRouteMissContentType,
			body:        legacyRouteMissBody,
			routed:      true,
		},
		{
			name:        "arbitrary plain-text 404",
			contentType: legacyRouteMissContentType,
			body:        "route not found\n",
		},
		{
			name:        "default-looking body with non-router content type",
			contentType: "application/octet-stream",
			body:        legacyRouteMissBody,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests++
				w.Header().Set("Content-Type", test.contentType)
				if test.routed {
					w.Header().Set(dependencyCallRoutedHeader, test.routedValue)
				}
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(test.body))
			}))
			defer srv.Close()
			t.Setenv("MS_DISPATCH_URL", srv.URL)
			t.Setenv("MS_LOCAL_MODULE_PROXY_URL", srv.URL)
			t.Setenv("MS_INTERNAL_SECRET", "caller-session-secret")

			m, err := New(Config{ID: "m14b4db3ac7f34e6a880ebc763cb3ca55", Slug: "consumer"})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			m.devMode = true
			m.DependsOn("video-transcode")
			lookups := 0
			m.devDir.lookup = func(context.Context, string) (devModuleEntry, bool, error) {
				lookups++
				return devModuleEntry{ModuleID: "m5ae9627818b74bd1ae23031455822070", Slug: "video-transcode"}, true, nil
			}

			ctx := auth.Set(context.Background(), auth.Identity{AppID: "a722a8a8-d413-435b-b21b-f4cbacb5ef73"})
			err = m.CallDependencyPost(ctx, "video-transcode", "/internal/jobs", map[string]string{"videoId": "v1"}, nil)
			if err == nil || !callStatusIs(err, http.StatusNotFound) {
				t.Fatalf("CallDependencyPost error = %v, want preserved 404", err)
			}
			if requests != 1 || lookups != 0 {
				t.Fatalf("404 replayed/consulted direct route: requests=%d lookups=%d", requests, lookups)
			}
		})
	}
}

func TestResolveDevModuleURL_DoesNotReuseProductionDispatch(t *testing.T) {
	t.Setenv("MS_DISPATCH_URL", "https://api.mirrorstack.example/dispatch")
	t.Setenv("MS_LOCAL_MODULE_PROXY_URL", "http://127.0.0.1:18080/")

	got := resolveDevModuleURL("video-transcode", "/internal/jobs?retry=1")
	want := "http://127.0.0.1:18080/_m/video-transcode/internal/jobs?retry=1"
	if got != want {
		t.Fatalf("resolveDevModuleURL = %q, want %q", got, want)
	}
	if strings.Contains(got, "api.mirrorstack.example") {
		t.Fatalf("local module endpoint reused production dispatch: %q", got)
	}
}

func TestCallDependency_DevUndeclaredRejectedBeforeDirectoryOrNetwork(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	t.Setenv("MS_DISPATCH_URL", srv.URL)
	t.Setenv("MS_INTERNAL_SECRET", "caller-session-secret")

	m, err := New(Config{ID: "m14b4db3ac7f34e6a880ebc763cb3ca55", Slug: "consumer"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m.devMode = true
	lookups := 0
	m.devDir.lookup = func(context.Context, string) (devModuleEntry, bool, error) {
		lookups++
		return devModuleEntry{ModuleID: "m5ae9627818b74bd1ae23031455822070", Slug: "video-transcode"}, true, nil
	}
	ctx := auth.Set(context.Background(), auth.Identity{AppID: "a722a8a8-d413-435b-b21b-f4cbacb5ef73"})
	err = m.CallDependencyPost(ctx, "video-transcode", "/internal/jobs", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "not declared") {
		t.Fatalf("undeclared call error = %v", err)
	}
	if lookups != 0 || requests != 0 {
		t.Fatalf("undeclared call performed I/O: lookups=%d requests=%d", lookups, requests)
	}
}

func TestCallDependency_DevUnauthorizedNeverFallsBack(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()
	t.Setenv("MS_DISPATCH_URL", srv.URL)
	t.Setenv("MS_INTERNAL_SECRET", "caller-session-secret")

	m, err := New(Config{ID: "m14b4db3ac7f34e6a880ebc763cb3ca55", Slug: "consumer"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m.devMode = true
	m.DependsOn("video-transcode")
	lookups := 0
	m.devDir.lookup = func(context.Context, string) (devModuleEntry, bool, error) {
		lookups++
		return devModuleEntry{ModuleID: "m5ae9627818b74bd1ae23031455822070", Slug: "video-transcode"}, true, nil
	}
	ctx := auth.Set(context.Background(), auth.Identity{AppID: "a722a8a8-d413-435b-b21b-f4cbacb5ef73"})
	err = m.CallDependencyPost(ctx, "video-transcode", "/internal/jobs", nil, nil)
	if err == nil || !callStatusIs(err, http.StatusUnauthorized) {
		t.Fatalf("unauthorized error = %v", err)
	}
	if requests != 1 || lookups != 0 {
		t.Fatalf("unauthorized call requests=%d lookups=%d, want one secure request and no lookup", requests, lookups)
	}
}

func TestCallDependency_DevDirectoryMissOrErrorPreservesIngress404(t *testing.T) {
	for _, test := range []struct {
		name      string
		lookupErr error
	}{
		{name: "missing or expired lease"},
		{name: "directory unavailable", lookupErr: errors.New("local database unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			var paths []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				http.NotFound(w, r)
			}))
			defer srv.Close()
			t.Setenv("MS_DISPATCH_URL", srv.URL)
			t.Setenv("MS_INTERNAL_SECRET", "caller-session-secret")

			const consumerID = "m14b4db3ac7f34e6a880ebc763cb3ca55"
			m, err := New(Config{ID: consumerID, Slug: "consumer"})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			m.devMode = true
			m.DependsOn("video-transcode")
			m.devDir.lookup = func(context.Context, string) (devModuleEntry, bool, error) {
				return devModuleEntry{}, false, test.lookupErr
			}
			const appID = "a722a8a8-d413-435b-b21b-f4cbacb5ef73"
			ctx := auth.Set(context.Background(), auth.Identity{AppID: appID})
			err = m.CallDependencyPost(ctx, "video-transcode", "/internal/jobs", nil, nil)
			if err == nil || !callStatusIs(err, http.StatusNotFound) {
				t.Fatalf("CallDependencyPost error = %v, want preserved ingress 404", err)
			}
			want := "/internal/apps/" + appID + "/module-calls/" + consumerID + "/video-transcode/internal/jobs"
			if len(paths) != 1 || paths[0] != want {
				t.Fatalf("requests = %q, want only %q", paths, want)
			}
		})
	}
}

func TestCallDependency_DevDeclarationNormalizesUUIDAndModuleID(t *testing.T) {
	m, err := New(Config{ID: "m14b4db3ac7f34e6a880ebc763cb3ca55"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m.DependsOn("m5ae9627818b74bd1ae23031455822070")
	if !m.declaresDependencyRef("5ae96278-18b7-4bd1-ae23-031455822070") {
		t.Fatal("dashed UUID did not match its declared m<hex> module ID")
	}
}

func TestCallDependency_RejectsUnsafeInputAndOversizedBody(t *testing.T) {
	m, err := New(Config{ID: "consumer_id"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := auth.Set(context.Background(), auth.Identity{AppID: "app-id"})
	for _, path := range []string{"/public/jobs", "/platform/jobs", "/internal/../platform/jobs", "/internal/jobs%2fadmin", "/internal/jobs\\escape"} {
		if err := m.CallDependencyPost(ctx, "video-transcode", path, nil, nil); err == nil || !strings.Contains(err.Error(), "canonical /internal") {
			t.Errorf("CallDependencyPost(%q) error = %v, want internal-path rejection", path, err)
		}
	}
	for _, producer := range []string{"", "@owner/", "owner/producer", "producer name"} {
		if err := m.CallDependencyPost(ctx, producer, "/internal/jobs", nil, nil); err == nil {
			t.Errorf("CallDependencyPost producer %q unexpectedly succeeded", producer)
		}
	}
	if err := m.CallDependencyPost(context.Background(), "video-transcode", "/internal/jobs", nil, nil); err == nil || !strings.Contains(err.Error(), "no app scope") {
		t.Fatalf("missing-app error = %v", err)
	}

	oversized := map[string]string{"value": strings.Repeat("x", maxDependencyCallBody)}
	if err := m.CallDependencyPost(ctx, "video-transcode", "/internal/jobs", oversized, nil); err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("oversized-body error = %v", err)
	}
}

func TestCall_SendsAppIDServiceSecretActorDelegationAndDecodesResponse(t *testing.T) {
	var gotMethod, gotPath, gotAppID, gotCT string
	var gotSecrets, gotDelegations []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAppID = r.Header.Get("X-MS-App-ID")
		gotCT = r.Header.Get("Content-Type")
		gotSecrets = r.Header.Values("X-MS-Service-Secret")
		gotDelegations = r.Header.Values(actor.HeaderDelegation)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("MS_DISPATCH_URL", srv.URL)
	t.Setenv("MS_INTERNAL_SECRET", "session-secret-1")

	m, _ := New(Config{ID: "demo"})
	ctx := auth.Set(context.Background(), auth.Identity{AppID: "a-456"})
	ctx = actor.WithDelegation(ctx, "msa1.payload.signature")

	var out struct {
		OK bool `json:"ok"`
	}
	if err := m.CallPost(ctx, "m0123", "/platform/exchange", map[string]string{"code": "x"}, &out); err != nil {
		t.Fatalf("CallPost: %v", err)
	}
	if !out.OK {
		t.Errorf("out.OK = false, want true")
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/module/m0123/platform/exchange" {
		t.Errorf("path = %q, want /module/m0123/platform/exchange", gotPath)
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
	if len(gotDelegations) != 1 || gotDelegations[0] != "msa1.payload.signature" {
		t.Errorf("%s values = %q, want exactly one trusted assertion", actor.HeaderDelegation, gotDelegations)
	}
}

func TestCall_OmitsActorDelegationOutsidePlatformRoutes(t *testing.T) {
	var requests [][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Header.Values(actor.HeaderDelegation))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	t.Setenv("MS_DISPATCH_URL", srv.URL)

	m, _ := New(Config{ID: "demo"})
	ctx := actor.WithDelegation(context.Background(), "msa1.payload.signature")
	for _, callPath := range []string{"/internal/exchange", "/public/catalog", "/platformish/not-platform"} {
		if err := m.CallGet(ctx, "m0123", callPath, nil); err != nil {
			t.Fatalf("CallGet(%q): %v", callPath, err)
		}
	}
	for index, values := range requests {
		if len(values) != 0 {
			t.Errorf("request %d actor values = %q, want none", index, values)
		}
	}
}

func TestCall_OmitsEmptyServiceSecret(t *testing.T) {
	var gotSecrets, gotDelegations []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSecrets = r.Header.Values("X-MS-Service-Secret")
		gotDelegations = r.Header.Values(actor.HeaderDelegation)
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
	if len(gotDelegations) != 0 {
		t.Errorf("%s values = %q, want no header", actor.HeaderDelegation, gotDelegations)
	}
}

func TestCall_Non2xxReturnsErrorWithTruncatedBody(t *testing.T) {
	const serviceSecret = "session-secret-must-not-leak"
	const actorDelegation = "msa1.actor-must-not-leak.signature"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream module unavailable"))
	}))
	defer srv.Close()
	t.Setenv("MS_DISPATCH_URL", srv.URL)
	t.Setenv("MS_INTERNAL_SECRET", serviceSecret)

	m, _ := New(Config{ID: "demo"})
	ctx := actor.WithDelegation(context.Background(), actorDelegation)
	err := m.CallGet(ctx, "m0123", "/internal/users", nil)
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
	if strings.Contains(msg, actorDelegation) {
		t.Errorf("error leaked %s: %q", actor.HeaderDelegation, msg)
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

// The inter-module client must not inherit DefaultTransport's 2-connection
// idle pool.
//
// 🔴 WHY IT MATTERS AND WHY IT IS INVISIBLE. Every inter-module call from a
// module goes to the SAME host (dispatch). DefaultTransport caps
// MaxIdleConnsPerHost at 2, so the moment a host module fans out to N
// contributors in parallel, connections 3..N pay a fresh TCP handshake (plus
// TLS in production) and are CLOSED rather than pooled — on every request.
// Most of the parallel win goes back into handshakes, and it presents as
// "parallelising didn't help much" rather than as a misconfigured pool.
//
// Asserted here rather than left to a comment because the symptom appears in a
// different repo (the module doing the fan-out) from the cause.
func TestCallHTTPPoolsEnoughConnectionsForFanOut(t *testing.T) {
	t.Parallel()

	transport, ok := callHTTP.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("callHTTP.Transport = %T, want *http.Transport — a nil transport "+
			"means DefaultTransport, which pools only 2 connections per host", callHTTP.Transport)
	}
	if transport.MaxIdleConnsPerHost <= 2 {
		t.Errorf("MaxIdleConnsPerHost = %d, want well above 2: every module call "+
			"targets the same dispatch host, so this IS the fan-out ceiling",
			transport.MaxIdleConnsPerHost)
	}
	// Cloned, not hand-built: proxy handling, timeouts and HTTP/2 must stay as
	// DefaultTransport had them.
	if def, isTransport := http.DefaultTransport.(*http.Transport); isTransport {
		if transport.IdleConnTimeout != def.IdleConnTimeout {
			t.Errorf("IdleConnTimeout = %v, want DefaultTransport's %v — the clone "+
				"should change pool sizes and nothing else",
				transport.IdleConnTimeout, def.IdleConnTimeout)
		}
	}
}
