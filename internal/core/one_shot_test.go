package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mirrorstack-ai/app-module-sdk/internal/runtime"
	"github.com/mirrorstack-ai/app-module-sdk/internal/taskenv"
	"github.com/mirrorstack-ai/app-module-sdk/meter"
)

const (
	oneShotTestJobID         = "11111111-1111-4111-8111-111111111111"
	oneShotTestAttemptID     = "22222222-2222-4222-8222-222222222222"
	oneShotTestModuleID      = "m5ae9627818b74bd1ae23031455822070"
	oneShotTestClaimModuleID = "5ae96278-18b7-4bd1-ae23-031455822070"
	oneShotTestModuleRef     = "video-transcode"
)

func newOneShotTestModule(t *testing.T) *Module {
	t.Helper()
	// One-shot attempts intentionally receive no ambient module/root secret.
	// Outbound operations must use only the broker-vended moduleCalls capability.
	t.Setenv("MS_INTERNAL_SECRET", "")
	m, err := New(Config{ID: oneShotTestModuleID, Slug: oneShotTestModuleRef})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func writeOneShotClaimFile(t *testing.T, claim runtime.ClaimResponse) string {
	t.Helper()
	raw, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "claim.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func oneShotCoreClaim() runtime.ClaimResponse {
	return runtime.ClaimResponse{
		Version: 1,
		Job:     runtime.BrokerJob{ID: oneShotTestJobID, AttemptID: oneShotTestAttemptID, Handler: "work", Deadline: time.Now().Add(time.Minute)},
		Workload: runtime.BrokerWorkload{
			AppID: "app_id", AppRef: "app_ref", ModuleID: oneShotTestClaimModuleID, ModuleRef: oneShotTestModuleRef, DispatchURL: "https://dispatch.example",
			VersionID: "version_id", Version: "1.0.0", AppSchema: "app_app_id", ModulePrefix: "module_prefix", Class: "heavy",
		},
		Payload:    json.RawMessage(`{"work":true}`),
		Capability: runtime.BrokerCapability{Token: "lease-token", ExpiresAt: time.Now().Add(time.Minute), RefreshAfter: time.Now().Add(30 * time.Second)},
	}
}

func TestStartOneShotPreclaimedHandoffSkipsClaimAndHidesFile(t *testing.T) {
	claim := oneShotCoreClaim()
	claimFile := writeOneShotClaimFile(t, claim)

	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Path)
		if !strings.HasSuffix(r.URL.Path, "/complete") {
			t.Errorf("unexpected broker path %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer lease-token" {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	t.Setenv(taskenv.BrokerURLVar, server.URL)
	t.Setenv(taskenv.JobIDVar, oneShotTestJobID)
	t.Setenv(taskenv.AttemptIDVar, oneShotTestAttemptID)
	t.Setenv(taskenv.BootstrapTokenVar, "")
	t.Setenv(taskenv.ClaimFileVar, claimFile)
	m := newOneShotTestModule(t)
	var calls atomic.Int32
	m.OnTask("work", func(context.Context, json.RawMessage) error {
		calls.Add(1)
		if os.Getenv(taskenv.ClaimFileVar) != "" || os.Getenv(taskenv.BootstrapTokenVar) != "" {
			t.Error("claim handoff remained visible to handler")
		}
		return nil
	})
	if err := m.startOneShot(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || len(requests) != 1 {
		t.Fatalf("handler calls=%d requests=%v", calls.Load(), requests)
	}
	if _, err := os.Stat(claimFile); !os.IsNotExist(err) {
		t.Fatalf("consumed claim file still exists: %v", err)
	}
}

func TestOneShotModuleCapabilityAuthenticatesDependencyCallAndChildTask(t *testing.T) {
	const childJobID = "33333333-3333-4333-8333-333333333333"
	claim := oneShotCoreClaim()
	claim.Resources.ModuleCalls = &runtime.ModuleCallCapability{Token: "stale-initial", ExpiresAt: time.Now().Add(5 * time.Second)}
	claimFile := writeOneShotClaimFile(t, claim)
	var refreshes, dependencyCalls, childTasks, terminalCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/resources/refresh"):
			refreshes.Add(1)
			if r.Header.Get("Authorization") != "Bearer lease-token" {
				t.Errorf("refresh authorization=%q", r.Header.Get("Authorization"))
			}
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"kinds":["moduleCalls"]`) {
				t.Errorf("refresh body=%s", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"v":1,"cancelled":false,"resources":{"moduleCalls":{"token":"task-service-cap","expiresAt":%q}}}`, time.Now().Add(time.Minute).Format(time.RFC3339Nano))
		case r.URL.Path == "/internal/apps/app_ref/module-calls/"+oneShotTestModuleID+"/video-core/internal/source":
			dependencyCalls.Add(1)
			if r.Header.Get("X-MS-Service-Secret") != "task-service-cap" {
				t.Errorf("dependency capability=%q", r.Header.Get("X-MS-Service-Secret"))
			}
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/internal/apps/app_ref/modules/"+oneShotTestModuleID+"/tasks/finalize":
			childTasks.Add(1)
			if r.Header.Get("X-MS-Service-Secret") != "task-service-cap" {
				t.Errorf("child task capability=%q", r.Header.Get("X-MS-Service-Secret"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"v":1,"jobId":%q,"status":"queued","deduplicated":false}`, childJobID)
		case r.URL.Path == "/apps/app_id/usage":
			if r.Header.Get("X-MS-Service-Secret") != "task-service-cap" {
				t.Errorf("meter capability=%q", r.Header.Get("X-MS-Service-Secret"))
			}
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/complete"):
			terminalCalls.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	claim.Workload.DispatchURL = server.URL
	claimFile = writeOneShotClaimFile(t, claim)
	t.Setenv(taskenv.BrokerURLVar, server.URL)
	t.Setenv(taskenv.JobIDVar, oneShotTestJobID)
	t.Setenv(taskenv.AttemptIDVar, oneShotTestAttemptID)
	t.Setenv(taskenv.BootstrapTokenVar, "")
	t.Setenv(taskenv.ClaimFileVar, claimFile)
	t.Setenv("MS_DISPATCH_URL", "http://127.0.0.1:1") // task context must override ambient routing.
	m := newOneShotTestModule(t)                      // ambient "secret" must never win.
	m.Meter("transcode.minutes", meter.Counter)
	m.OnTask("finalize", func(context.Context, json.RawMessage) error { return nil })
	m.OnTask("work", func(ctx context.Context, _ json.RawMessage) error {
		if err := m.CallDependencyPost(ctx, "video-core", "/internal/source", map[string]string{"videoId": "v1"}, nil); err != nil {
			return err
		}
		jobID, err := m.RunTask(ctx, "finalize", json.RawMessage(`{"videoId":"v1"}`))
		if err != nil {
			return err
		}
		if jobID != childJobID {
			return fmt.Errorf("child job ID=%q", jobID)
		}
		if err := m.Record(ctx, "transcode.minutes", 1); err != nil {
			return err
		}
		return nil
	})
	if err := m.startOneShot(); err != nil {
		t.Fatal(err)
	}
	if refreshes.Load() != 1 || dependencyCalls.Load() != 1 || childTasks.Load() != 1 || terminalCalls.Load() != 1 {
		t.Fatalf("refresh=%d dependency=%d child=%d terminal=%d", refreshes.Load(), dependencyCalls.Load(), childTasks.Load(), terminalCalls.Load())
	}
}

func TestOneShotModuleCapabilityFailsClosedWithoutOrAfterRefreshDenial(t *testing.T) {
	for _, test := range []struct {
		name          string
		refreshStatus int
		refreshBody   string
	}{
		{name: "missing", refreshStatus: http.StatusOK, refreshBody: `{"v":1,"cancelled":false,"resources":{}}`},
		{name: "denied", refreshStatus: http.StatusForbidden, refreshBody: `{"v":1,"code":"refresh_denied"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			claim := oneShotCoreClaim()
			var claimFile string
			var dispatchCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/resources/refresh"):
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(test.refreshStatus)
					_, _ = w.Write([]byte(test.refreshBody))
				case strings.HasSuffix(r.URL.Path, "/fail"):
					w.WriteHeader(http.StatusNoContent)
				default:
					dispatchCalls.Add(1)
					http.Error(w, "ambient capability was used", http.StatusForbidden)
				}
			}))
			defer server.Close()
			claim.Workload.DispatchURL = server.URL
			claimFile = writeOneShotClaimFile(t, claim)
			t.Setenv(taskenv.BrokerURLVar, server.URL)
			t.Setenv(taskenv.JobIDVar, oneShotTestJobID)
			t.Setenv(taskenv.AttemptIDVar, oneShotTestAttemptID)
			t.Setenv(taskenv.BootstrapTokenVar, "")
			t.Setenv(taskenv.ClaimFileVar, claimFile)
			t.Setenv("MS_DISPATCH_URL", "http://127.0.0.1:1") // task context must override ambient routing.
			m := newOneShotTestModule(t)
			m.OnTask("work", func(ctx context.Context, _ json.RawMessage) error {
				err := m.CallDependencyPost(ctx, "video-core", "/internal/source", nil, nil)
				if err == nil {
					return errors.New("missing capability unexpectedly authorized")
				}
				return err
			})
			if err := m.startOneShot(); err != nil {
				t.Fatal(err)
			}
			if dispatchCalls.Load() != 0 {
				t.Fatalf("dispatch calls=%d; ambient MS_INTERNAL_SECRET was used", dispatchCalls.Load())
			}
		})
	}
}
