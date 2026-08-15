package runtime

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

	"github.com/mirrorstack-ai/app-module-sdk/cache"
	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/storage"
)

const (
	testJobID         = "11111111-1111-4111-8111-111111111111"
	testAttemptID     = "22222222-2222-4222-8222-222222222222"
	testModuleID      = "m5ae9627818b74bd1ae23031455822070"
	testClaimModuleID = "5ae96278-18b7-4bd1-ae23-031455822070"
	testModuleRef     = "video-transcode"
)

type brokerRecorder struct {
	mu             sync.Mutex
	claim          ClaimResponse
	claimStatus    int
	claimCode      string
	heartbeat      HeartbeatResponse
	callbackStatus int
	callbackFails  int
	refreshStatus  int
	refreshDelay   time.Duration
	refresh        BrokerResources
	paths          []string
	auth           []string
	bodies         [][]byte
}

func validClaim() ClaimResponse {
	return ClaimResponse{
		Version: 1,
		Job:     BrokerJob{ID: testJobID, AttemptID: testAttemptID, Handler: "work", Deadline: time.Now().Add(time.Minute)},
		Workload: BrokerWorkload{
			AppID: "app1", AppRef: "app1", ModuleID: testClaimModuleID, ModuleRef: testModuleRef, DispatchURL: "https://dispatch.example",
			VersionID: "version-id", Version: "1.0.0", AppSchema: "app_app1", ModulePrefix: "mod_prefix", Class: "heavy",
		},
		Payload:    json.RawMessage(` {"x":1} `),
		Capability: BrokerCapability{Token: "lease-one", ExpiresAt: time.Now().Add(time.Minute), RefreshAfter: time.Now().Add(time.Second)},
	}
}

func (r *brokerRecorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.paths = append(r.paths, req.URL.Path)
	r.auth = append(r.auth, req.Header.Get("Authorization"))
	r.bodies = append(r.bodies, body)
	r.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasSuffix(req.URL.Path, "/claim"):
		if r.claimStatus != 0 {
			w.WriteHeader(r.claimStatus)
			_ = json.NewEncoder(w).Encode(map[string]any{"v": 1, "code": r.claimCode})
			return
		}
		_ = json.NewEncoder(w).Encode(r.claim)
	case strings.HasSuffix(req.URL.Path, "/heartbeat"):
		out := r.heartbeat
		if out.Version == 0 {
			out = HeartbeatResponse{Version: 1, Deadline: time.Now().Add(time.Minute)}
		}
		_ = json.NewEncoder(w).Encode(out)
	case strings.HasSuffix(req.URL.Path, "/resources/refresh"):
		if r.refreshDelay > 0 {
			time.Sleep(r.refreshDelay)
		}
		if r.refreshStatus != 0 {
			w.WriteHeader(r.refreshStatus)
			_, _ = w.Write([]byte(`{"v":1,"code":"refresh_denied"}`))
			return
		}
		resources := r.refresh
		if resources == (BrokerResources{}) {
			resources = r.claim.Resources
		}
		_ = json.NewEncoder(w).Encode(RefreshResponse{Version: 1, Resources: resources})
	case strings.HasSuffix(req.URL.Path, "/complete") || strings.HasSuffix(req.URL.Path, "/fail"):
		if r.callbackFails > 0 {
			r.callbackFails--
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"v":1,"code":"temporarily_unavailable"}`))
			return
		}
		if r.callbackStatus != 0 {
			w.WriteHeader(r.callbackStatus)
			_, _ = w.Write([]byte(`{"v":1,"code":"callback_failed"}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, req)
	}
}

func TestRunOneShotResourceRefreshIsCoalesced(t *testing.T) {
	claim := validClaim()
	claim.Resources.Database = &db.Credential{Host: "db", Port: 5432, Database: "app", Username: "role", Token: "old", ExpiresAt: time.Now().Add(taskResourceRefreshMargin / 2)}
	r := &brokerRecorder{claim: claim, refreshDelay: 20 * time.Millisecond, refresh: BrokerResources{
		Database: &db.Credential{Host: "db", Port: 5432, Database: "app", Username: "role", Token: "fresh", ExpiresAt: time.Now().Add(time.Hour)},
	}}
	err := runWithRecorder(t, r, map[string]TaskEntry{"work": {Handler: func(ctx context.Context, _ json.RawMessage) error {
		provider := db.CredentialProviderFrom(ctx)
		if provider == nil {
			return errors.New("missing provider")
		}
		if moduleProvider := db.ModuleCredentialProviderFrom(ctx); moduleProvider == nil {
			return errors.New("missing ModuleDB provider")
		} else if moduleProvider.(databaseProvider).manager != provider.(databaseProvider).manager {
			return errors.New("DB and ModuleDB providers do not share the scoped credential")
		}
		const n = 20
		errs := make(chan error, n)
		var wg sync.WaitGroup
		for range n {
			wg.Add(1)
			go func() { defer wg.Done(); _, err := provider.Credential(ctx); errs <- err }()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				return err
			}
		}
		return nil
	}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	refreshes := 0
	for _, path := range r.paths {
		if strings.HasSuffix(path, "/resources/refresh") {
			refreshes++
		}
	}
	if refreshes != 1 {
		t.Fatalf("refresh calls=%d paths=%v", refreshes, r.paths)
	}
}

func TestRunOneShotUsesFreshClaimResourcesWithoutRefresh(t *testing.T) {
	claim := validClaim()
	claim.Resources.Database = &db.Credential{
		Host: "db", Port: 5432, Database: "app", Username: "role", Token: "db-token", ExpiresAt: time.Now().Add(time.Hour),
	}
	claim.Resources.ModuleCalls = &ModuleCallCapability{Token: "call-token", ExpiresAt: time.Now().Add(time.Hour)}
	r := &brokerRecorder{claim: claim}
	err := runWithRecorder(t, r, map[string]TaskEntry{"work": {Handler: func(ctx context.Context, _ json.RawMessage) error {
		for range 2 {
			if _, err := db.CredentialProviderFrom(ctx).Credential(ctx); err != nil {
				return err
			}
			if _, err := ModuleCallCapabilityProviderFrom(ctx).ModuleCallCapability(ctx); err != nil {
				return err
			}
		}
		return nil
	}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range r.paths {
		if strings.HasSuffix(path, "/resources/refresh") {
			t.Fatalf("fresh claim resource unexpectedly refreshed: paths=%v", r.paths)
		}
	}
}

func TestRunOneShotRefreshesInitialStorageWithoutSessionToken(t *testing.T) {
	claim := validClaim()
	claim.Resources.Storage = &storage.Credential{
		Bucket:          "bucket",
		Region:          "ap-northeast-1",
		Prefix:          "apps/a/m/",
		CDNBase:         "https://cdn.example",
		AccessKeyID:     "initial-key",
		SecretAccessKey: "initial-secret",
		ExpiresAt:       time.Now().Add(time.Hour),
	}
	r := &brokerRecorder{claim: claim, refresh: BrokerResources{Storage: &storage.Credential{
		Bucket:          "bucket",
		Region:          "ap-northeast-1",
		Prefix:          "apps/a/m/",
		CDNBase:         "https://cdn.example",
		AccessKeyID:     "fresh-key",
		SecretAccessKey: "fresh-secret",
		SessionToken:    "fresh-session",
		ExpiresAt:       time.Now().Add(time.Hour),
	}}}
	err := runWithRecorder(t, r, map[string]TaskEntry{"work": {Handler: func(ctx context.Context, _ json.RawMessage) error {
		provider := storage.CredentialProviderFrom(ctx)
		for range 2 {
			credential, err := provider.Credential(ctx)
			if err != nil {
				return err
			}
			if credential.SessionToken != "fresh-session" {
				return fmt.Errorf("storage session token was not refreshed")
			}
		}
		return nil
	}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	refreshes := 0
	for _, path := range r.paths {
		if strings.HasSuffix(path, "/resources/refresh") {
			refreshes++
		}
	}
	if refreshes != 1 {
		t.Fatalf("refresh calls=%d paths=%v", refreshes, r.paths)
	}
}

func TestRunOneShotNearExpiryResourcesRefreshOncePerKind(t *testing.T) {
	claim := validClaim()
	claim.Resources.Database = &db.Credential{
		Host: "db", Port: 5432, Database: "app", Username: "role", Token: "old-db", ExpiresAt: time.Now().Add(taskResourceRefreshMargin / 2),
	}
	claim.Resources.ModuleCalls = &ModuleCallCapability{Token: "old-call", ExpiresAt: time.Now().Add(taskResourceRefreshMargin / 2)}
	r := &brokerRecorder{claim: claim, refreshDelay: 20 * time.Millisecond, refresh: BrokerResources{
		Database:    &db.Credential{Host: "db", Port: 5432, Database: "app", Username: "role", Token: "fresh-db", ExpiresAt: time.Now().Add(time.Hour)},
		ModuleCalls: &ModuleCallCapability{Token: "fresh-call", ExpiresAt: time.Now().Add(time.Hour)},
	}}
	err := runWithRecorder(t, r, map[string]TaskEntry{"work": {Handler: func(ctx context.Context, _ json.RawMessage) error {
		const n = 20
		var wg sync.WaitGroup
		errs := make(chan error, n*2)
		for range n {
			wg.Add(2)
			go func() {
				defer wg.Done()
				_, err := db.CredentialProviderFrom(ctx).Credential(ctx)
				errs <- err
			}()
			go func() {
				defer wg.Done()
				_, err := ModuleCallCapabilityProviderFrom(ctx).ModuleCallCapability(ctx)
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				return err
			}
		}
		return nil
	}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for i, path := range r.paths {
		if !strings.HasSuffix(path, "/resources/refresh") {
			continue
		}
		var body struct {
			Kinds []string `json:"kinds"`
		}
		if err := json.Unmarshal(r.bodies[i], &body); err != nil || len(body.Kinds) != 1 {
			t.Fatalf("refresh body=%s err=%v", r.bodies[i], err)
		}
		counts[body.Kinds[0]]++
	}
	if counts["database"] != 1 || counts["moduleCalls"] != 1 {
		t.Fatalf("refresh counts=%v paths=%v", counts, r.paths)
	}
}

func TestRunOneShotRenewsAllResourceKindsWithoutAmbientFallback(t *testing.T) {
	now := time.Now()
	claim := validClaim()
	claim.Resources = BrokerResources{
		Database: &db.Credential{
			Host: "db", Port: 5432, Database: "app", Username: "role", Token: "expired-db", ExpiresAt: now.Add(-time.Minute),
		},
		Cache: &cache.Credential{
			Endpoint: "cache:6379", Username: "role", Token: "expired-cache", ExpiresAt: now.Add(-time.Minute),
		},
		Storage: &storage.Credential{
			Bucket: "bucket", Region: "ap-northeast-1", Prefix: "apps/a/m/", CDNBase: "https://cdn.example",
			AccessKeyID: "expired-key", SecretAccessKey: "expired-secret", SessionToken: "expired-session", ExpiresAt: now.Add(-time.Minute),
		},
		ModuleCalls: &ModuleCallCapability{Token: "expired-call", ExpiresAt: now.Add(-time.Minute)},
	}
	refreshed := claim.Resources
	refreshed.Database = &db.Credential{Host: "db", Port: 5432, Database: "app", Username: "role", Token: "fresh-db", ExpiresAt: now.Add(time.Hour)}
	refreshed.Cache = &cache.Credential{Endpoint: "cache:6379", Username: "role", Token: "fresh-cache", ExpiresAt: now.Add(time.Hour)}
	refreshed.Storage = &storage.Credential{
		Bucket: "bucket", Region: "ap-northeast-1", Prefix: "apps/a/m/", CDNBase: "https://cdn.example",
		AccessKeyID: "fresh-key", SecretAccessKey: "fresh-secret", SessionToken: "fresh-session", ExpiresAt: now.Add(time.Hour),
	}
	refreshed.ModuleCalls = &ModuleCallCapability{Token: "fresh-call", ExpiresAt: now.Add(time.Hour)}

	var mu sync.Mutex
	kinds := make(map[string]int)
	var terminal atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(req.URL.Path, "/claim"):
			_ = json.NewEncoder(w).Encode(claim)
		case strings.HasSuffix(req.URL.Path, "/resources/refresh"):
			if req.Header.Get("Authorization") != "Bearer lease-one" {
				t.Errorf("refresh authorization=%q", req.Header.Get("Authorization"))
			}
			var body struct {
				Version int      `json:"v"`
				Lease   string   `json:"leaseToken"`
				Kinds   []string `json:"kinds"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil || body.Version != 1 || body.Lease != "lease-one" || len(body.Kinds) != 1 {
				t.Fatalf("refresh body=%+v err=%v", body, err)
			}
			mu.Lock()
			kinds[body.Kinds[0]]++
			mu.Unlock()
			response := RefreshResponse{Version: 1}
			switch body.Kinds[0] {
			case "database":
				response.Resources.Database = refreshed.Database
			case "cache":
				response.Resources.Cache = refreshed.Cache
			case "storage":
				response.Resources.Storage = refreshed.Storage
			case "moduleCalls":
				response.Resources.ModuleCalls = refreshed.ModuleCalls
			default:
				t.Errorf("unexpected refresh kind %q", body.Kinds[0])
			}
			_ = json.NewEncoder(w).Encode(response)
		case strings.HasSuffix(req.URL.Path, "/complete"):
			terminal.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()
	client, err := NewBrokerClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	err = RunOneShot(context.Background(), OneShotConfig{
		Broker: client, JobID: testJobID, AttemptID: testAttemptID, BootstrapToken: "bootstrap-secret", ModuleID: testModuleID, ModuleRef: testModuleRef,
		Handlers: map[string]TaskEntry{"work": {Handler: func(ctx context.Context, _ json.RawMessage) error {
			database, err := db.CredentialProviderFrom(ctx).Credential(ctx)
			if err != nil || database.Token != "fresh-db" {
				return fmt.Errorf("database credential=%q: %w", database.Token, err)
			}
			redis, err := cache.CredentialProviderFrom(ctx).Credential(ctx)
			if err != nil || redis.Token != "fresh-cache" {
				return fmt.Errorf("cache credential=%q: %w", redis.Token, err)
			}
			objectStore, err := storage.CredentialProviderFrom(ctx).Credential(ctx)
			if err != nil || objectStore.AccessKeyID != "fresh-key" {
				return fmt.Errorf("storage credential=%q: %w", objectStore.AccessKeyID, err)
			}
			calls, err := ModuleCallCapabilityProviderFrom(ctx).ModuleCallCapability(ctx)
			if err != nil || calls.Token != "fresh-call" {
				return fmt.Errorf("module-call capability=%q: %w", calls.Token, err)
			}
			return nil
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, kind := range []string{"database", "cache", "storage", "moduleCalls"} {
		if kinds[kind] != 1 {
			t.Fatalf("refresh counts=%v", kinds)
		}
	}
	if terminal.Load() != 1 {
		t.Fatalf("terminal callbacks=%d", terminal.Load())
	}
}

func TestRunOneShotRefreshFailureCancelsHandler(t *testing.T) {
	claim := validClaim()
	claim.Resources.Database = &db.Credential{Host: "db", Port: 5432, Database: "app", Username: "role", Token: "token"}
	r := &brokerRecorder{claim: claim, refreshStatus: http.StatusForbidden}
	var cancelled atomic.Bool
	err := runWithRecorder(t, r, map[string]TaskEntry{"work": {Handler: func(ctx context.Context, _ json.RawMessage) error {
		_, refreshErr := db.CredentialProviderFrom(ctx).Credential(ctx)
		<-ctx.Done()
		cancelled.Store(true)
		return refreshErr
	}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !cancelled.Load() || !strings.HasSuffix(r.paths[len(r.paths)-1], "/fail") {
		t.Fatalf("cancelled=%v paths=%v", cancelled.Load(), r.paths)
	}
}

func runWithRecorder(t *testing.T, recorder *brokerRecorder, handlers map[string]TaskEntry, heartbeat time.Duration) error {
	t.Helper()
	server := newTestBrokerServer(t, recorder)
	client, err := NewBrokerClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return RunOneShot(context.Background(), OneShotConfig{Broker: client, JobID: testJobID, AttemptID: testAttemptID, BootstrapToken: "bootstrap-secret", ModuleID: testModuleID, ModuleRef: testModuleRef, Handlers: handlers, HeartbeatEvery: heartbeat})
}

func newTestBrokerServer(t *testing.T, recorder *brokerRecorder) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(recorder)
	t.Cleanup(server.Close)
	return server
}

func TestRunOneShotExactHandlerAndComplete(t *testing.T) {
	r := &brokerRecorder{claim: validClaim()}
	var work, other atomic.Int32
	err := runWithRecorder(t, r, map[string]TaskEntry{
		"work": {Handler: func(_ context.Context, payload json.RawMessage) error {
			work.Add(1)
			if string(payload) != `{"x":1}` {
				t.Errorf("payload=%q", payload)
			}
			return nil
		}},
		"other": {Handler: func(context.Context, json.RawMessage) error { other.Add(1); return nil }},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if work.Load() != 1 || other.Load() != 0 {
		t.Fatalf("work=%d other=%d", work.Load(), other.Load())
	}
	if len(r.paths) != 2 || r.paths[1][len(r.paths[1])-8:] != "complete" {
		t.Fatalf("paths=%v", r.paths)
	}
	if r.auth[0] != "Bearer bootstrap-secret" || r.auth[1] != "Bearer lease-one" {
		t.Fatalf("auth sequence=%v", r.auth)
	}
}

func TestRunOneShotDuplicateClaimDoesNotExecute(t *testing.T) {
	r := &brokerRecorder{claimStatus: http.StatusConflict, claimCode: "attempt_already_handled"}
	var calls atomic.Int32
	err := runWithRecorder(t, r, map[string]TaskEntry{"work": {Handler: func(context.Context, json.RawMessage) error { calls.Add(1); return nil }}}, 0)
	if err != nil || calls.Load() != 0 || len(r.paths) != 1 {
		t.Fatalf("err=%v calls=%d paths=%v", err, calls.Load(), r.paths)
	}
}

func TestBrokerClaimOnlyTreatsExactHandledEnvelopeAsDuplicate(t *testing.T) {
	for _, test := range []struct {
		name, body string
		wantDone   bool
	}{
		{name: "canonical handled", body: `{"v":1,"code":"attempt_already_handled"}`, wantDone: true},
		{name: "generic conflict", body: `{"v":1,"code":"conflict"}`},
		{name: "legacy error field", body: `{"error":"attempt_already_handled"}`},
		{name: "legacy code without version", body: `{"code":"attempt_already_handled"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/claim") {
					t.Fatalf("path=%q", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			client, err := NewBrokerClient(server.URL, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Claim(context.Background(), testJobID, testAttemptID, "bootstrap")
			if errors.Is(err, ErrAttemptAlreadyHandled) != test.wantDone {
				t.Fatalf("Claim error=%v handled=%v, want handled=%v", err, errors.Is(err, ErrAttemptAlreadyHandled), test.wantDone)
			}
			if !test.wantDone && err == nil {
				t.Fatal("non-canonical 409 unexpectedly succeeded")
			}
		})
	}
}

func TestRunOneShotMismatchedClaimFailsWithoutExecution(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*ClaimResponse)
	}{
		{name: "module id", mutate: func(claim *ClaimResponse) { claim.Workload.ModuleID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			claim := validClaim()
			test.mutate(&claim)
			r := &brokerRecorder{claim: claim}
			var calls atomic.Int32
			err := runWithRecorder(t, r, map[string]TaskEntry{"work": {Handler: func(context.Context, json.RawMessage) error { calls.Add(1); return nil }}}, 0)
			if err == nil || calls.Load() != 0 || len(r.paths) != 1 {
				t.Fatalf("err=%v calls=%d paths=%v", err, calls.Load(), r.paths)
			}
		})
	}
}

// A module's slug is a display label and renaming it is a supported operation.
// Identity is the canonical module UUID, so a claim that carries the new (or
// old) ref must still execute — pinning the ref would strand every in-flight
// task of a renamed module in the no-callback branch until the watchdog expired
// it, once per retry.
func TestRunOneShotAcceptsRenamedModuleRefWithMatchingModuleID(t *testing.T) {
	claim := validClaim()
	claim.Workload.ModuleRef = "video-transcode-renamed"
	r := &brokerRecorder{claim: claim}
	var calls atomic.Int32
	err := runWithRecorder(t, r, map[string]TaskEntry{"work": {Handler: func(context.Context, json.RawMessage) error { calls.Add(1); return nil }}}, 0)
	if err != nil || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d paths=%v", err, calls.Load(), r.paths)
	}
}

func TestRunOneShotRejectsUntrustedDispatchURL(t *testing.T) {
	for _, raw := range []string{"", "http://dispatch.example", "https://user:pass@dispatch.example", "https://dispatch.example/?query=1", "https://dispatch.example/"} {
		t.Run(raw, func(t *testing.T) {
			claim := validClaim()
			claim.Workload.DispatchURL = raw
			r := &brokerRecorder{claim: claim}
			var calls atomic.Int32
			err := runWithRecorder(t, r, map[string]TaskEntry{"work": {Handler: func(context.Context, json.RawMessage) error { calls.Add(1); return nil }}}, 0)
			if err == nil || calls.Load() != 0 {
				t.Fatalf("dispatchURL=%q err=%v calls=%d", raw, err, calls.Load())
			}
		})
	}
}

type permanentTestError struct{ error }

func (permanentTestError) Retryable() bool { return false }

func TestRunOneShotFailureClassificationAndExactlyOneCallback(t *testing.T) {
	tests := []struct {
		name, code string
		handler    TaskHandlerFunc
		retryable  bool
	}{
		{"ordinary", "handler_error", func(context.Context, json.RawMessage) error { return errors.New("boom") }, true},
		{"permanent", "permanent_error", func(context.Context, json.RawMessage) error { return permanentTestError{errors.New("bad input")} }, false},
		{"panic", "handler_panic", func(context.Context, json.RawMessage) error { panic("boom") }, true},
		{"deadline", "deadline_exceeded", func(ctx context.Context, _ json.RawMessage) error { <-ctx.Done(); return ctx.Err() }, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := &brokerRecorder{claim: validClaim()}
			entry := TaskEntry{Handler: test.handler}
			if test.name == "deadline" {
				entry.Timeout = time.Millisecond
			}
			if err := runWithRecorder(t, r, map[string]TaskEntry{"work": entry}, 0); err != nil {
				t.Fatal(err)
			}
			if len(r.paths) != 2 || r.paths[1][len(r.paths[1])-4:] != "fail" {
				t.Fatalf("paths=%v", r.paths)
			}
			var body struct {
				Code      string `json:"code"`
				Retryable bool   `json:"retryable"`
			}
			if err := json.Unmarshal(r.bodies[1], &body); err != nil {
				t.Fatal(err)
			}
			if body.Code != test.code || body.Retryable != test.retryable {
				t.Fatalf("body=%s", r.bodies[1])
			}
		})
	}
}

func TestRunOneShotHeartbeatRotatesCapabilityAndCancelsHandler(t *testing.T) {
	claim := validClaim()
	r := &brokerRecorder{claim: claim, heartbeat: HeartbeatResponse{Version: 1, Cancelled: true, Capability: &BrokerCapability{
		Token: "lease-two", ExpiresAt: time.Now().Add(time.Minute), RefreshAfter: time.Now().Add(30 * time.Second),
	}}}
	var calls atomic.Int32
	err := runWithRecorder(t, r, map[string]TaskEntry{"work": {Handler: func(ctx context.Context, _ json.RawMessage) error { calls.Add(1); <-ctx.Done(); return ctx.Err() }}}, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || len(r.paths) != 3 {
		t.Fatalf("calls=%d paths=%v", calls.Load(), r.paths)
	}
	if got := r.auth[len(r.auth)-1]; got != "Bearer lease-two" {
		t.Fatalf("terminal auth=%q", got)
	}
}

func TestRunOneShotCallbackFailureDoesNotRerunHandler(t *testing.T) {
	r := &brokerRecorder{claim: validClaim(), callbackStatus: http.StatusServiceUnavailable}
	var calls atomic.Int32
	err := runWithRecorder(t, r, map[string]TaskEntry{"work": {Handler: func(context.Context, json.RawMessage) error { calls.Add(1); return nil }}}, 0)
	if err == nil || calls.Load() != 1 || len(r.paths) != 4 {
		t.Fatalf("err=%v calls=%d paths=%v", err, calls.Load(), r.paths)
	}
}

func TestRunOneShotRetriesAmbiguousTerminalWithoutRerunningHandler(t *testing.T) {
	r := &brokerRecorder{claim: validClaim(), callbackFails: 1}
	var calls atomic.Int32
	err := runWithRecorder(t, r, map[string]TaskEntry{"work": {Handler: func(context.Context, json.RawMessage) error {
		calls.Add(1)
		return nil
	}}}, 0)
	if err != nil || calls.Load() != 1 || len(r.paths) != 3 {
		t.Fatalf("err=%v calls=%d paths=%v", err, calls.Load(), r.paths)
	}
	if !strings.HasSuffix(r.paths[1], "/complete") || !strings.HasSuffix(r.paths[2], "/complete") {
		t.Fatalf("terminal retry paths=%v", r.paths)
	}
}
