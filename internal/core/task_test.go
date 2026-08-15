package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/db"
)

func TestOnTask_Registration(t *testing.T) {
	m := newTestModuleWithSecret(t, "test")

	called := false
	m.OnTask("transcode", func(ctx context.Context, payload json.RawMessage) error {
		called = true
		return nil
	}, WithTimeout(10*time.Minute), WithMaxRetries(3))

	if _, ok := m.taskHandlers["transcode"]; !ok {
		t.Fatal("task handler not registered in taskHandlers map")
	}
	if called {
		t.Error("handler should not be called at registration time")
	}
}

func TestPermanentTaskErrorPreservesErrorIdentity(t *testing.T) {
	sentinel := errors.New("bad input")
	err := Permanent(sentinel)
	if !IsPermanent(err) || !errors.Is(err, sentinel) {
		t.Fatalf("permanent wrapper lost identity: %v", err)
	}
	if Permanent(nil) != nil {
		t.Fatal("Permanent(nil) must remain nil")
	}
}

func TestOnTask_DuplicatePanics(t *testing.T) {
	m := newTestModuleWithSecret(t, "test")
	m.OnTask("work", func(ctx context.Context, p json.RawMessage) error { return nil })

	assertPanics(t, "expected panic on duplicate OnTask registration", func() {
		m.OnTask("work", func(ctx context.Context, p json.RawMessage) error { return nil })
	})
}

func TestOnTask_InvalidNamePanics(t *testing.T) {
	m := newTestModuleWithSecret(t, "test")

	bad := []string{"", "has/slash", "has space", "has..dots"}
	for _, name := range bad {
		t.Run(name, func(t *testing.T) {
			assertPanics(t, "expected panic for invalid task name "+name, func() {
				m.OnTask(name, func(ctx context.Context, p json.RawMessage) error { return nil })
			})
		})
	}
}

func TestOnTask_AppearsInManifest(t *testing.T) {
	m := newTestModuleWithSecret(t, "test")
	m.OnTask("transcode", func(ctx context.Context, p json.RawMessage) error { return nil },
		WithTimeout(10*time.Minute), WithMaxRetries(3))

	rec := doRequestWithSecret(t, m.Router(), "GET", "/__mirrorstack/platform/manifest", "secret")
	if rec.Code != 200 {
		t.Fatalf("manifest status = %d, want 200", rec.Code)
	}

	var payload struct {
		Tasks []struct {
			Name        string `json:"name"`
			MaxDuration string `json:"maxDuration"`
			MaxRetries  int    `json:"maxRetries"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(payload.Tasks) != 1 {
		t.Fatalf("tasks count = %d, want 1", len(payload.Tasks))
	}
	task := payload.Tasks[0]
	if task.Name != "transcode" {
		t.Errorf("task name = %q, want transcode", task.Name)
	}
	if task.MaxDuration != "10m0s" {
		t.Errorf("task maxDuration = %q, want 10m0s", task.MaxDuration)
	}
	if task.MaxRetries != 3 {
		t.Errorf("task maxRetries = %d, want 3", task.MaxRetries)
	}
}

// The default must not move: a task declared before WithCompute existed keeps a
// byte-identical manifest entry, so no module lands on a different runner (or a
// different bill) because the SDK grew a field.
func TestOnTask_NoComputeLeavesManifestByteIdentical(t *testing.T) {
	m := newTestModuleWithSecret(t, "test")
	m.OnTask("transcode", func(context.Context, json.RawMessage) error { return nil },
		WithTimeout(10*time.Minute), WithMaxRetries(3))

	got, err := json.Marshal(m.registry.Tasks()[0])
	if err != nil {
		t.Fatalf("marshal task: %v", err)
	}
	const want = `{"name":"transcode","maxDuration":"10m0s","maxRetries":3}`
	if string(got) != want {
		t.Errorf("task JSON = %s, want %s", got, want)
	}
	if strings.Contains(string(got), "compute") {
		t.Errorf("task JSON unexpectedly carries a compute key: %s", got)
	}
}

func TestOnTask_WithComputeProjectsManifest(t *testing.T) {
	tests := []struct {
		name    string
		compute Compute
		want    string
	}{
		{
			name:    "heavy carries its dimensions",
			compute: Heavy(Res{VCPU: 4, MemoryMB: 8192}),
			want:    `{"name":"work","compute":{"class":"heavy","vcpu":4,"memoryMb":8192}}`,
		},
		{
			// GPU dimensions are RESOLVED from the instance, not requested, so
			// the app owner approves the box they will actually be billed for.
			name:    "gpu resolves dimensions from the instance",
			compute: GPU(Res{Instance: "g5g.xlarge"}),
			want:    `{"name":"work","compute":{"class":"gpu","vcpu":4,"memoryMb":7168,"instance":"g5g.xlarge"}}`,
		},
		{
			// An explicit Standard declaration says "Lambda, platform default"
			// out loud; it must not invent a size.
			name:    "explicit standard default carries class only",
			compute: Standard(Res{}),
			want:    `{"name":"work","compute":{"class":"standard"}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := newTestModuleWithSecret(t, "test")
			m.OnTask("work", func(context.Context, json.RawMessage) error { return nil }, WithCompute(test.compute))
			got, err := json.Marshal(m.registry.Tasks()[0])
			if err != nil {
				t.Fatalf("marshal task: %v", err)
			}
			if string(got) != test.want {
				t.Errorf("task JSON = %s, want %s", got, test.want)
			}
			if strings.Contains(string(got), `"vcpu":0`) || strings.Contains(string(got), `"memoryMb":0`) {
				t.Errorf("task JSON contains zero-valued resolved dimensions: %s", got)
			}
		})
	}
}

func TestOnTask_ComputeAndEphemeralManifestCanonical(t *testing.T) {
	m := newTestModuleWithSecret(t, "test")
	m.OnTask("transcode", func(context.Context, json.RawMessage) error { return nil },
		WithTimeout(2*time.Hour), WithMaxRetries(2),
		WithCompute(Heavy(Res{VCPU: 4, MemoryMB: 8192})),
		WithEphemeralStorage(80*GiB))
	got, err := json.Marshal(m.registry.Tasks()[0])
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"name":"transcode","maxDuration":"2h0m0s","maxRetries":2,"compute":{"class":"heavy","vcpu":4,"memoryMb":8192},"ephemeralStorageMb":81920}`
	if string(got) != want {
		t.Fatalf("task JSON = %s, want %s", got, want)
	}
}

func TestOnTask_RejectsInvalidTimeoutAndEphemeralStorage(t *testing.T) {
	tests := []struct {
		name    string
		options []TaskOption
	}{
		{"standard over 15 minutes", []TaskOption{WithTimeout(15*time.Minute + time.Second)}},
		{"fractional-second timeout", []TaskOption{WithTimeout(time.Second + time.Millisecond)}},
		{"managed over 24 hours", []TaskOption{WithCompute(Heavy(Res{VCPU: 4, MemoryMB: 8192})), WithTimeout(24*time.Hour + time.Second)}},
		{"negative timeout", []TaskOption{WithTimeout(-time.Second)}},
		{"retry ceiling", []TaskOption{WithMaxRetries(11)}},
		{"standard ephemeral", []TaskOption{WithEphemeralStorage(80 * GiB)}},
		{"unaligned", []TaskOption{WithCompute(Heavy(Res{VCPU: 4, MemoryMB: 8192})), WithEphemeralStorage(21*GiB + MiB)}},
		{"fargate gap", []TaskOption{WithCompute(Heavy(Res{VCPU: 4, MemoryMB: 8192})), WithEphemeralStorage(20*GiB + MiB)}},
		{"too large", []TaskOption{WithCompute(Heavy(Res{VCPU: 4, MemoryMB: 8192})), WithEphemeralStorage(201 * GiB)}},
		{"gpu scratch is platform owned", []TaskOption{WithCompute(GPU(Res{Instance: "g5g.xlarge"})), WithEphemeralStorage(80 * GiB)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := newTestModuleWithSecret(t, "test")
			assertPanics(t, "expected invalid task declaration to panic", func() { m.OnTask("work", func(context.Context, json.RawMessage) error { return nil }, test.options...) })
		})
	}
}

func TestOnTask_RejectsNameOver128Bytes(t *testing.T) {
	m := newTestModuleWithSecret(t, "test")
	assertPanics(t, "expected overlong task name to panic", func() {
		m.OnTask(strings.Repeat("x", 129), func(context.Context, json.RawMessage) error { return nil })
	})
}

// A Compute that did not come from a constructor was never validated, so it
// must not reach the manifest as a runner class the platform cannot resolve.
func TestWithCompute_RejectsUnvalidatedDescriptor(t *testing.T) {
	message := panicMessage(func() { WithCompute(Compute{}) })
	if message == "" {
		t.Fatal("WithCompute(Compute{}) did not panic")
	}
	for _, want := range []string{"ms.Standard", "ms.Heavy", "ms.GPU"} {
		if !strings.Contains(message, want) {
			t.Errorf("panic %q does not point at %s", message, want)
		}
	}
}

func TestOnTask_ManifestEmptyTasksNotNull(t *testing.T) {
	m := newTestModuleWithSecret(t, "test")

	rec := doRequestWithSecret(t, m.Router(), "GET", "/__mirrorstack/platform/manifest", "secret")
	if rec.Code != 200 {
		t.Fatalf("manifest status = %d, want 200", rec.Code)
	}

	// Verify "tasks":[] not "tasks":null
	body := rec.Body.String()
	if !strings.Contains(body, `"tasks":[]`) {
		t.Errorf("manifest should contain \"tasks\":[], got: %s", body)
	}
}

func TestOnTask_DevHTTPEndpoint(t *testing.T) {
	m := newTestModuleWithSecret(t, "test")

	var received json.RawMessage
	m.OnTask("echo", func(ctx context.Context, payload json.RawMessage) error {
		received = payload
		return nil
	})

	body := strings.NewReader(`{"key":"value"}`)
	req := httptest.NewRequest("POST", "/__mirrorstack/tasks/echo", body)
	req.Header.Set("X-MS-Internal-Secret", "secret")
	rec := httptest.NewRecorder()
	m.Router().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	if string(received) != `{"key":"value"}` {
		t.Errorf("received payload = %s, want {\"key\":\"value\"}", string(received))
	}
}

func TestOnTask_DevHTTPEndpoint_RequiresAuth(t *testing.T) {
	m := newTestModuleWithSecret(t, "test")
	m.OnTask("work", func(ctx context.Context, p json.RawMessage) error { return nil })

	rec := doRequest(t, m.Router(), "POST", "/__mirrorstack/tasks/work")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 without internal secret", rec.Code)
	}
}

func TestOnTask_DevHTTPEndpoint_HandlerError(t *testing.T) {
	m := newTestModuleWithSecret(t, "test")
	m.OnTask("fail", func(ctx context.Context, p json.RawMessage) error {
		return context.DeadlineExceeded
	})

	req := httptest.NewRequest("POST", "/__mirrorstack/tasks/fail", strings.NewReader(`{}`))
	req.Header.Set("X-MS-Internal-Secret", "secret")
	rec := httptest.NewRecorder()
	m.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 on handler error", rec.Code)
	}
}

func TestOnTask_WithTimeout(t *testing.T) {
	m := newTestModuleWithSecret(t, "test")
	m.OnTask("slow", func(ctx context.Context, p json.RawMessage) error { return nil },
		WithTimeout(5*time.Second))

	entry := m.taskHandlers["slow"]
	if entry.timeout != 5*time.Second {
		t.Errorf("timeout = %v, want 5s", entry.timeout)
	}
}

// --- RunTask (dev-mode in-process dispatch) ---

func TestRunTask_DevMode_Dispatches(t *testing.T) {
	m := newTestModuleWithSecret(t, "test")
	m.devMode = true

	received := make(chan json.RawMessage, 1)
	m.OnTask("echo", func(ctx context.Context, payload json.RawMessage) error {
		received <- payload
		return nil
	})

	payload := json.RawMessage(`{"key":"value"}`)
	taskID, err := m.RunTask(context.Background(), "echo", payload)
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if taskID == "" {
		t.Error("RunTask should return a non-empty taskID")
	}
	select {
	case got := <-received:
		if string(got) != `{"key":"value"}` {
			t.Errorf("received = %s, want {\"key\":\"value\"}", string(got))
		}
	case <-time.After(time.Second):
		t.Fatal("local task handler did not execute")
	}
}

func TestRunTask_UnknownTask(t *testing.T) {
	m := newTestModuleWithSecret(t, "test")

	_, err := m.RunTask(context.Background(), "nonexistent", nil)
	if err == nil {
		t.Error("RunTask should error on unknown task")
	}
}

func TestRunTask_PayloadTooLarge(t *testing.T) {
	m := newTestModuleWithSecret(t, "test")
	m.OnTask("work", func(ctx context.Context, p json.RawMessage) error { return nil })

	big := make(json.RawMessage, 300*1024) // > 256KB
	_, err := m.RunTask(context.Background(), "work", big)
	if err == nil {
		t.Error("RunTask should reject payload > 256KB")
	}
}

func TestRunTask_DevMode_HandlerErrorMarksFailed(t *testing.T) {
	m := newTestModuleWithSecret(t, "test")
	m.devMode = true
	m.OnTask("fail", func(ctx context.Context, p json.RawMessage) error {
		return errors.New("boom")
	})

	jobID, err := m.RunTask(context.Background(), "fail", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("RunTask returned handler error synchronously: %v", err)
	}
	waitForLocalTaskStatus(t, m, jobID, localTaskFailed)
}

func TestRunTask_ManagedTransportContractAndRetry(t *testing.T) {
	const jobID = "11111111-1111-4111-8111-111111111111"
	var calls int
	var firstKey string
	payload := json.RawMessage(" \n {\"videoId\": \"v1\"} \t")
	canonicalPayload := json.RawMessage(`{"videoId":"v1"}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/internal/apps/app_ref/modules/test/tasks/transcode" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("X-MS-Service-Secret"); got != "module-secret" {
			t.Errorf("service secret = %q", got)
		}
		key := r.Header.Get("Idempotency-Key")
		if !uuidPattern.MatchString(key) {
			t.Errorf("idempotency key = %q", key)
		}
		if calls == 1 {
			firstKey = key
		} else if key != firstKey {
			t.Errorf("retry key changed: %q -> %q", firstKey, key)
		}
		body, _ := io.ReadAll(r.Body)
		want := append([]byte(`{"payload":`), canonicalPayload...)
		want = append(want, '}')
		if string(body) != string(want) {
			t.Errorf("body = %q, want %q", body, want)
		}
		for _, forbidden := range []string{"resources", "actor", "userId", "appSchema", "sqs", "credential"} {
			if strings.Contains(strings.ToLower(string(body)), strings.ToLower(forbidden)) {
				t.Errorf("body contains forbidden field %q: %s", forbidden, body)
			}
		}
		if calls == 1 {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"v":1,"jobId":"` + jobID + `","status":"queued","deduplicated":true}`))
	}))
	defer server.Close()
	t.Setenv("MS_DISPATCH_URL", server.URL)
	t.Setenv("MS_INTERNAL_SECRET", "module-secret")
	m, err := New(Config{ID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	m.OnTask("transcode", func(context.Context, json.RawMessage) error {
		t.Fatal("managed enqueue ran handler locally")
		return nil
	})
	ctx := auth.Set(context.Background(), auth.Identity{AppID: "app_ref"})
	got, err := m.RunTask(ctx, "transcode", payload)
	if err != nil {
		t.Fatal(err)
	}
	if got != jobID || calls != 2 {
		t.Fatalf("job=%q calls=%d", got, calls)
	}
}

func TestRunTask_ManagedIdempotentRecoveryAcceptsExistingState(t *testing.T) {
	const jobID = "11111111-1111-4111-8111-111111111111"
	for _, status := range []string{"queued", "running", "succeeded", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"v":1,"jobId":%q,"status":%q,"deduplicated":true}`, jobID, status)
			}))
			defer server.Close()
			t.Setenv("MS_DISPATCH_URL", server.URL)
			t.Setenv("MS_INTERNAL_SECRET", "module-secret")
			m, _ := New(Config{ID: "test"})
			m.OnTask("work", func(context.Context, json.RawMessage) error { return nil })
			ctx := auth.Set(context.Background(), auth.Identity{AppID: "app_ref"})
			got, err := m.RunTaskWithIdempotencyKey(ctx, "work", json.RawMessage(`{}`), "22222222-2222-4222-8222-222222222222")
			if err != nil || got != jobID {
				t.Fatalf("state=%q job=%q err=%v", status, got, err)
			}
		})
	}
}

func TestRunTaskWithIdempotencyKeyRecoversAcrossCallsAndRejectsPayloadConflict(t *testing.T) {
	const (
		jobID = "11111111-1111-4111-8111-111111111111"
		key   = "22222222-2222-4222-8222-222222222222"
	)
	var firstBody []byte
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := r.Header.Get("Idempotency-Key"); got != key {
			t.Errorf("idempotency key = %q, want %q", got, key)
		}
		body, _ := io.ReadAll(r.Body)
		if firstBody == nil {
			firstBody = append([]byte(nil), body...)
		} else if string(body) != string(firstBody) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"v":1,"code":"idempotency_reuse"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"v":1,"jobId":%q,"status":"queued","deduplicated":%t}`, jobID, calls > 1)
	}))
	defer server.Close()
	t.Setenv("MS_DISPATCH_URL", server.URL)
	t.Setenv("MS_INTERNAL_SECRET", "module-secret")
	m, _ := New(Config{ID: "test"})
	m.OnTask("work", func(context.Context, json.RawMessage) error { return nil })
	ctx := auth.Set(context.Background(), auth.Identity{AppID: "app_ref"})
	payload := json.RawMessage(`{"logicalJob":"video-1"}`)

	first, err := m.RunTaskWithIdempotencyKey(ctx, "work", payload, key)
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.RunTaskWithIdempotencyKey(ctx, "work", payload, key)
	if err != nil {
		t.Fatal(err)
	}
	if first != jobID || second != jobID {
		t.Fatalf("job IDs = %q, %q; want recovered %q", first, second, jobID)
	}
	if _, err := m.RunTaskWithIdempotencyKey(ctx, "work", json.RawMessage(`{"logicalJob":"video-2"}`), key); err == nil || !strings.Contains(err.Error(), "HTTP 409") {
		t.Fatalf("different-payload key reuse error = %v", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestRunTaskWithIdempotencyKeyRequiresCanonicalUUID(t *testing.T) {
	m := newTestModuleWithSecret(t, "test")
	m.OnTask("work", func(context.Context, json.RawMessage) error { return nil })
	for _, key := range []string{"", "not-a-uuid", "11111111-1111-1111-1111-111111111111", "11111111-1111-4111-7111-111111111111"} {
		if _, err := m.RunTaskWithIdempotencyKey(context.Background(), "work", nil, key); err == nil {
			t.Errorf("idempotency key %q unexpectedly accepted", key)
		}
	}
}

func TestRunTask_ManagedDeterministic4xxDoesNotRetry(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; http.Error(w, "denied", http.StatusForbidden) }))
	defer server.Close()
	t.Setenv("MS_DISPATCH_URL", server.URL)
	t.Setenv("MS_INTERNAL_SECRET", "module-secret")
	m, _ := New(Config{ID: "test"})
	m.OnTask("work", func(context.Context, json.RawMessage) error { return nil })
	_, err := m.RunTask(auth.Set(context.Background(), auth.Identity{AppID: "app_ref"}), "work", json.RawMessage(`{}`))
	if err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d, want deterministic single failure", err, calls)
	}
}

func TestRunTask_ManagedRejectsMalformedAndOversizedResponses(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{"malformed", []byte(`{"v":1,"status":"queued"}`)},
		{"oversized", make([]byte, managedTaskResponseLimit+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(test.body)
			}))
			defer server.Close()
			t.Setenv("MS_DISPATCH_URL", server.URL)
			t.Setenv("MS_INTERNAL_SECRET", "module-secret")
			m, _ := New(Config{ID: "test"})
			m.OnTask("work", func(context.Context, json.RawMessage) error { return nil })
			_, err := m.RunTask(auth.Set(context.Background(), auth.Identity{AppID: "app_ref"}), "work", json.RawMessage(`{}`))
			if err == nil {
				t.Fatal("expected response to fail closed")
			}
		})
	}
}

func TestRunTask_ManagedFailsClosed(t *testing.T) {
	tests := []struct {
		name                  string
		app, dispatch, secret string
	}{
		{"app scope", "", "http://example.test", "secret"},
		{"dispatch URL", "app", "", "secret"},
		{"module credential", "app", "http://example.test", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("MS_DISPATCH_URL", test.dispatch)
			t.Setenv("MS_INTERNAL_SECRET", test.secret)
			m, _ := New(Config{ID: "test"})
			m.OnTask("work", func(context.Context, json.RawMessage) error { return nil })
			var ctx context.Context = context.Background()
			if test.app != "" {
				ctx = auth.Set(ctx, auth.Identity{AppID: test.app})
			}
			if _, err := m.RunTask(ctx, "work", json.RawMessage(`{}`)); err == nil {
				t.Fatal("expected fail-closed error")
			}
		})
	}
}

func TestRunTask_LocalExecutorRequiresExplicitDevMode(t *testing.T) {
	t.Setenv("MS_TASK_QUEUE_URL", "https://unused.example/queue")
	m := newTestModuleWithSecret(t, "test")
	m.devMode = true
	called := make(chan struct{}, 1)
	m.OnTask("work", func(context.Context, json.RawMessage) error { called <- struct{}{}; return nil })
	if _, err := m.RunTask(context.Background(), "work", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("explicit dev executor did not run")
	}
}

func TestRunTask_DevMode_ReturnsImmediatelyAndDetachesRequestCancellation(t *testing.T) {
	const key = "11111111-1111-4111-8111-111111111111"
	m := newTestModuleWithSecret(t, "test")
	m.devMode = true
	started := make(chan struct{})
	inspect := make(chan struct{})
	release := make(chan struct{})
	observed := make(chan struct {
		schema string
		err    error
	}, 1)
	m.OnTask("slow", func(ctx context.Context, _ json.RawMessage) error {
		close(started)
		<-inspect
		observed <- struct {
			schema string
			err    error
		}{schema: db.SchemaFrom(ctx), err: ctx.Err()}
		<-release
		return nil
	})

	requestCtx, requestCancel := context.WithCancel(db.WithSchema(context.Background(), "app_preserved"))
	type result struct {
		jobID string
		err   error
	}
	returned := make(chan result, 1)
	go func() {
		jobID, err := m.RunTaskWithIdempotencyKey(requestCtx, "slow", json.RawMessage(`{"x":1}`), key)
		returned <- result{jobID: jobID, err: err}
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	select {
	case got := <-returned:
		if got.err != nil || got.jobID != key {
			t.Fatalf("RunTask result = (%q, %v), want stable key", got.jobID, got.err)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("RunTask waited for the local handler")
	}

	if got := waitForLocalTaskStatus(t, m, key, localTaskRunning); got.Cancelled {
		t.Fatalf("running status marked cancelled: %+v", got)
	}
	requestCancel()
	close(inspect)
	got := <-observed
	if got.schema != "app_preserved" || got.err != nil {
		t.Fatalf("detached context = {schema:%q err:%v}", got.schema, got.err)
	}
	close(release)
	waitForLocalTaskStatus(t, m, key, localTaskSucceeded)
}

func TestRunTask_DevMode_IdempotencyMatchesManagedCanonicalJSON(t *testing.T) {
	const key = "22222222-2222-4222-8222-222222222222"
	m := newTestModuleWithSecret(t, "test")
	m.devMode = true
	release := make(chan struct{})
	var executions atomic.Int32
	m.OnTask("work", func(context.Context, json.RawMessage) error {
		executions.Add(1)
		<-release
		return nil
	})
	m.OnTask("other", func(context.Context, json.RawMessage) error {
		executions.Add(100)
		return nil
	})
	payload := json.RawMessage(" \n {\"z\": 2, \"x\":1} \t")

	const callers = 16
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			jobID, err := m.RunTaskWithIdempotencyKey(context.Background(), "work", payload, key)
			if err == nil && jobID != key {
				err = fmt.Errorf("job ID = %q, want %q", jobID, key)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	waitForLocalTaskStatus(t, m, key, localTaskRunning)

	if jobID, err := m.RunTaskWithIdempotencyKey(context.Background(), "work", json.RawMessage(`{"x":1,"z":2}`), key); err != nil || jobID != key {
		t.Fatalf("canonically equivalent payload retry = (%q, %v), want existing job", jobID, err)
	}
	if _, err := m.RunTaskWithIdempotencyKey(context.Background(), "work", json.RawMessage(`{"x":1,"z":3}`), key); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("different-value payload reuse error = %v", err)
	}
	if _, err := m.RunTaskWithIdempotencyKey(context.Background(), "other", payload, key); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("different-name reuse error = %v", err)
	}
	if got := executions.Load(); got != 1 {
		t.Fatalf("handler executions = %d, want exactly 1", got)
	}
	close(release)
	waitForLocalTaskStatus(t, m, key, localTaskSucceeded)
}

func TestRunTask_DevMode_IdempotencyAndControlAreAppScoped(t *testing.T) {
	const key = "23232323-2323-4232-8232-232323232323"
	m := newTestModuleWithSecret(t, "test")
	m.devMode = true
	release := make(chan struct{})
	var executions atomic.Int32
	m.OnTask("work", func(ctx context.Context, _ json.RawMessage) error {
		executions.Add(1)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			return nil
		}
	})
	appA := auth.Set(context.Background(), auth.Identity{AppID: "app-a"})
	appB := auth.Set(context.Background(), auth.Identity{AppID: "app-b"})
	for _, ctx := range []context.Context{appA, appB} {
		jobID, err := m.RunTaskWithIdempotencyKey(ctx, "work", json.RawMessage(`{"x":1}`), key)
		if err != nil || jobID != key {
			t.Fatalf("RunTaskWithIdempotencyKey = (%q, %v)", jobID, err)
		}
	}
	waitForLocalTaskStatusContext(t, appA, m, key, localTaskRunning)
	waitForLocalTaskStatusContext(t, appB, m, key, localTaskRunning)
	if got := executions.Load(); got != 2 {
		t.Fatalf("handler executions=%d, want one per app", got)
	}
	if err := m.CancelTask(appA, key); err != nil {
		t.Fatal(err)
	}
	waitForLocalTaskStatusContext(t, appA, m, key, localTaskCancelled)
	if got, err := m.TaskStatus(appB, key); err != nil || got.Status != localTaskRunning {
		t.Fatalf("app B status after app A cancellation = (%+v, %v)", got, err)
	}
	close(release)
	waitForLocalTaskStatusContext(t, appB, m, key, localTaskSucceeded)
}

func TestCancelTask_DevMode_QueuedAndRunningAreIdempotent(t *testing.T) {
	const (
		queuedID  = "33333333-3333-4333-8333-333333333333"
		runningID = "44444444-4444-4444-8444-444444444444"
	)
	m := newTestModuleWithSecret(t, "test")
	m.devMode = true
	if _, _, err := m.localTasks.enqueue("", queuedID, "queued", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if got := waitForLocalTaskStatus(t, m, queuedID, localTaskQueued); got.Cancelled {
		t.Fatalf("queued status marked cancelled: %+v", got)
	}
	if err := m.CancelTask(context.Background(), queuedID); err != nil {
		t.Fatal(err)
	}
	if err := m.CancelTask(context.Background(), queuedID); err != nil {
		t.Fatalf("second queued cancellation: %v", err)
	}
	if got := waitForLocalTaskStatus(t, m, queuedID, localTaskCancelled); !got.Cancelled {
		t.Fatalf("cancelled status missing cancellation marker: %+v", got)
	}

	started := make(chan struct{})
	cancelled := make(chan error, 1)
	m.OnTask("running", func(ctx context.Context, _ json.RawMessage) error {
		close(started)
		<-ctx.Done()
		cancelled <- ctx.Err()
		return ctx.Err()
	})
	if _, err := m.RunTaskWithIdempotencyKey(context.Background(), "running", json.RawMessage(`{}`), runningID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("running handler did not start")
	}
	waitForLocalTaskStatus(t, m, runningID, localTaskRunning)
	if err := m.CancelTask(context.Background(), runningID); err != nil {
		t.Fatal(err)
	}
	if err := m.CancelTask(context.Background(), runningID); err != nil {
		t.Fatalf("second running cancellation: %v", err)
	}
	if err := <-cancelled; !errors.Is(err, context.Canceled) {
		t.Fatalf("handler cancellation = %v", err)
	}
	if got := waitForLocalTaskStatus(t, m, runningID, localTaskCancelled); !got.Cancelled {
		t.Fatalf("cancelled status missing cancellation marker: %+v", got)
	}
}

func TestCancelTask_DevMode_PreservesTerminalStates(t *testing.T) {
	m := newTestModuleWithSecret(t, "test")
	m.devMode = true
	tests := []struct {
		name   string
		key    string
		err    error
		status string
	}{
		{"success", "55555555-5555-4555-8555-555555555555", nil, localTaskSucceeded},
		{"failure", "66666666-6666-4666-8666-666666666666", errors.New("failed"), localTaskFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m.OnTask(test.name, func(context.Context, json.RawMessage) error { return test.err })
			if _, err := m.RunTaskWithIdempotencyKey(context.Background(), test.name, json.RawMessage(`{}`), test.key); err != nil {
				t.Fatal(err)
			}
			waitForLocalTaskStatus(t, m, test.key, test.status)
			if err := m.CancelTask(context.Background(), test.key); err != nil {
				t.Fatal(err)
			}
			waitForLocalTaskStatus(t, m, test.key, test.status)
		})
	}
}

func TestRunTask_DevMode_TimeoutAndPanicSettleTerminally(t *testing.T) {
	m := newTestModuleWithSecret(t, "test")
	m.devMode = true
	m.OnTask("timeout", func(ctx context.Context, _ json.RawMessage) error {
		<-ctx.Done()
		return ctx.Err()
	}, WithTimeout(time.Second))
	m.OnTask("panic", func(context.Context, json.RawMessage) error { panic("boom") })

	timeoutID, err := m.RunTask(context.Background(), "timeout", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	waitForLocalTaskStatus(t, m, timeoutID, localTaskFailed)
	panicID, err := m.RunTask(context.Background(), "panic", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	waitForLocalTaskStatus(t, m, panicID, localTaskFailed)
}

func waitForLocalTaskStatus(t *testing.T, m *Module, jobID, want string) TaskJobStatus {
	return waitForLocalTaskStatusContext(t, context.Background(), m, jobID, want)
}

func waitForLocalTaskStatusContext(t *testing.T, ctx context.Context, m *Module, jobID, want string) TaskJobStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		got, err := m.TaskStatus(ctx, jobID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == want {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s status = %q, want %q", jobID, got.Status, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestTaskStatusManagedContract(t *testing.T) {
	const jobID = "11111111-1111-4111-8111-111111111111"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.Path != "/internal/apps/app_ref/modules/test/task-jobs/"+jobID {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("X-MS-Service-Secret"); got != "module-secret" {
			t.Errorf("service secret = %q", got)
		}
		if got := r.Header.Get("Idempotency-Key"); got != "" {
			t.Errorf("GET idempotency key = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"v":1,"jobId":"` + jobID + `","status":"running","cancelled":false}`))
	}))
	defer server.Close()
	t.Setenv("MS_DISPATCH_URL", server.URL)
	t.Setenv("MS_INTERNAL_SECRET", "module-secret")
	m, _ := New(Config{ID: "test"})
	got, err := m.TaskStatus(auth.Set(context.Background(), auth.Identity{AppID: "app_ref"}), jobID)
	if err != nil {
		t.Fatal(err)
	}
	if got.JobID != jobID || got.Status != "running" || got.Cancelled {
		t.Fatalf("status = %+v", got)
	}
}

func TestCancelTaskManagedContractRetriesWithStableKey(t *testing.T) {
	const jobID = "11111111-1111-4111-8111-111111111111"
	var calls int
	var idempotencyKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/internal/apps/app_ref/modules/test/task-jobs/"+jobID+"/cancel" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-MS-Service-Secret"); got != "module-secret" {
			t.Errorf("service secret = %q", got)
		}
		key := r.Header.Get("Idempotency-Key")
		if !uuidPattern.MatchString(key) {
			t.Errorf("idempotency key = %q", key)
		}
		if calls == 1 {
			idempotencyKey = key
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		if key != idempotencyKey {
			t.Errorf("retry changed idempotency key: %q -> %q", idempotencyKey, key)
		}
		body, _ := io.ReadAll(r.Body)
		if len(body) != 0 {
			t.Errorf("cancel body = %q, want empty", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"v":1,"jobId":"` + jobID + `","status":"cancelled","cancelled":true}`))
	}))
	defer server.Close()
	t.Setenv("MS_DISPATCH_URL", server.URL)
	t.Setenv("MS_INTERNAL_SECRET", "module-secret")
	m, _ := New(Config{ID: "test"})
	if err := m.CancelTask(auth.Set(context.Background(), auth.Identity{AppID: "app_ref"}), jobID); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestCancelTaskAcceptsQueuedRunningAndTerminalStates(t *testing.T) {
	const jobID = "11111111-1111-4111-8111-111111111111"
	for _, status := range []string{"queued", "running", "succeeded", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"v":1,"jobId":%q,"status":%q,"cancelled":%t}`, jobID, status, status == "cancelled")
			}))
			defer server.Close()
			t.Setenv("MS_DISPATCH_URL", server.URL)
			t.Setenv("MS_INTERNAL_SECRET", "module-secret")
			m, _ := New(Config{ID: "test"})
			ctx := auth.Set(context.Background(), auth.Identity{AppID: "app_ref"})
			if err := m.CancelTask(ctx, jobID); err != nil {
				t.Fatalf("terminal/idempotent cancellation failed: %v", err)
			}
		})
	}
}

func TestCancelTaskDeterministic404DoesNotRetry(t *testing.T) {
	const jobID = "11111111-1111-4111-8111-111111111111"
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()
	t.Setenv("MS_DISPATCH_URL", server.URL)
	t.Setenv("MS_INTERNAL_SECRET", "module-secret")
	m, _ := New(Config{ID: "test"})
	ctx := auth.Set(context.Background(), auth.Identity{AppID: "app_ref"})
	if err := m.CancelTask(ctx, jobID); err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
