package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func writeClaimFile(t *testing.T, value any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claim.json")
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPreclaimedFileSkipsClaimAndSendsOneTerminalCallback(t *testing.T) {
	recorder := &brokerRecorder{claim: validClaim()}
	server := newTestBrokerServer(t, recorder)
	client, err := NewBrokerClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	claim, err := LoadClaimFile(writeClaimFile(t, validClaim()))
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	err = RunOneShot(context.Background(), OneShotConfig{
		Broker: client, JobID: testJobID, AttemptID: testAttemptID,
		Preclaimed: &claim, ModuleID: testModuleID, ModuleRef: testModuleRef,
		Handlers: map[string]TaskEntry{"work": {Handler: func(context.Context, json.RawMessage) error {
			calls.Add(1)
			return nil
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("handler calls=%d", calls.Load())
	}
	if len(recorder.paths) != 1 || !strings.HasSuffix(recorder.paths[0], "/complete") {
		t.Fatalf("broker paths=%v; want zero claim calls and one complete", recorder.paths)
	}
	if recorder.auth[0] != "Bearer lease-one" {
		t.Fatalf("terminal auth=%q", recorder.auth[0])
	}
}

func TestClaimFileMalformedOversizedAndUnprotectedFailClosed(t *testing.T) {
	tests := []struct {
		name  string
		write func(*testing.T) string
	}{
		{"malformed", func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "claim.json")
			if err := os.WriteFile(path, []byte(`{"v":1`), 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{"oversized", func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "claim.json")
			if err := os.WriteFile(path, make([]byte, claimFileLimit+1), 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{"group-readable", func(t *testing.T) string {
			path := writeClaimFile(t, validClaim())
			if err := os.Chmod(path, 0o640); err != nil {
				t.Fatal(err)
			}
			return path
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := LoadClaimFile(test.write(t)); err == nil {
				t.Fatal("expected claim file to fail closed")
			}
		})
	}
}

func TestPreclaimedMismatchAndExpiryFailBeforeHandler(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ClaimResponse)
	}{
		{"job mismatch", func(c *ClaimResponse) { c.Job.ID = "33333333-3333-4333-8333-333333333333" }},
		{"attempt mismatch", func(c *ClaimResponse) { c.Job.AttemptID = "33333333-3333-4333-8333-333333333333" }},
		{"module id mismatch", func(c *ClaimResponse) { c.Workload.ModuleID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" }},
		{"expired deadline", func(c *ClaimResponse) { c.Job.Deadline = c.Job.Deadline.Add(-2 * time.Minute) }},
		{"empty capability", func(c *ClaimResponse) { c.Capability.Token = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			claim := validClaim()
			test.mutate(&claim)
			loaded, err := LoadClaimFile(writeClaimFile(t, claim))
			if err != nil {
				t.Fatal(err)
			}
			recorder := &brokerRecorder{claim: validClaim()}
			server := newTestBrokerServer(t, recorder)
			client, _ := NewBrokerClient(server.URL, server.Client())
			var calls atomic.Int32
			err = RunOneShot(context.Background(), OneShotConfig{
				Broker: client, JobID: testJobID, AttemptID: testAttemptID, Preclaimed: &loaded,
				ModuleID: testModuleID, ModuleRef: testModuleRef, Handlers: map[string]TaskEntry{"work": {Handler: func(context.Context, json.RawMessage) error { calls.Add(1); return nil }}},
			})
			if err == nil || calls.Load() != 0 || len(recorder.paths) != 0 {
				t.Fatalf("err=%v handler calls=%d broker paths=%v", err, calls.Load(), recorder.paths)
			}
		})
	}
}
