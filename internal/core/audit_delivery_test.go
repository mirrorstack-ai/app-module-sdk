package core

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mirrorstack-ai/app-module-sdk/internal/auditoutbox"
	"github.com/mirrorstack-ai/app-module-sdk/internal/invocationwire"
)

func auditDeliveryFixture(t *testing.T) (auditoutbox.Delivery, []byte, string) {
	t.Helper()
	raw, err := os.ReadFile("../../invocation/testdata/invocation_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.TrimSpace(raw)
	inv, err := invocationwire.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return auditoutbox.Delivery{
		ID: 1, EventID: "77777777-7777-4777-8777-777777777777",
		OccurredAt:  inv.Request.OccurredAt.Add(time.Second),
		SubjectKind: "user", SubjectID: "44444444-4444-4444-8444-444444444444",
		Action: "updated", Details: json.RawMessage(`{"field":"displayName"}`),
		Proof: raw, Attempts: 1, LeaseToken: "88888888-8888-4888-8888-888888888888",
	}, raw, inv.App.ID
}

func TestDeliverAuditUsesOnlyStoredProvenanceAndRenewableAuthority(t *testing.T) {
	delivery, proof, appID := auditDeliveryFixture(t)
	var gotCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCalls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/apps/"+appID+"/audit" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-MS-App-ID"); got != appID {
			t.Errorf("X-MS-App-ID = %q, want %q", got, appID)
		}
		if got := r.Header.Get("X-MS-Service-Secret"); got != "audit-service-secret" {
			t.Errorf("X-MS-Service-Secret = %q", got)
		}
		_, gotProof, err := invocationwire.DecodeHeader(r.Header.Get(invocationwire.Header))
		if err != nil || !bytes.Equal(gotProof, proof) {
			t.Errorf("invocation proof changed: equal=%v err=%v", bytes.Equal(gotProof, proof), err)
		}
		body, _ := io.ReadAll(r.Body)
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Errorf("body: %v", err)
			return
		}
		for _, forbidden := range []string{"appId", "moduleId", "actor", "userId", "invocation", "provenance"} {
			if _, ok := envelope[forbidden]; ok {
				t.Errorf("caller-authored trust field %q reached the body", forbidden)
			}
		}
		if string(envelope["eventId"]) != `"`+delivery.EventID+`"` ||
			string(envelope["details"]) != string(delivery.Details) {
			t.Errorf("body = %s", body)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	defer server.Close()
	t.Setenv("MS_DISPATCH_URL", server.URL)
	t.Setenv("MS_INTERNAL_SECRET", "audit-service-secret")

	disposition, code, err := deliverAudit(context.Background(), delivery)
	if err != nil || disposition != auditAcknowledge || code != "" || gotCalls.Load() != 1 {
		t.Fatalf("deliver = disposition:%d code:%q err:%v calls:%d", disposition, code, err, gotCalls.Load())
	}
}

func TestDeliverAuditClassifiesResponsesWithoutLeakingBodies(t *testing.T) {
	delivery, _, _ := auditDeliveryFixture(t)
	for _, tc := range []struct {
		status int
		want   auditDisposition
	}{
		{400, auditQuarantine}, {409, auditQuarantine}, {413, auditQuarantine}, {422, auditQuarantine},
		{401, auditRetry}, {403, auditRetry}, {404, auditRetry}, {408, auditRetry},
		{425, auditRetry}, {429, auditRetry}, {500, auditRetry}, {503, auditRetry},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte("private-response-body"))
			}))
			defer server.Close()
			t.Setenv("MS_DISPATCH_URL", server.URL)
			t.Setenv("MS_INTERNAL_SECRET", "audit-service-secret")
			disposition, code, err := deliverAudit(context.Background(), delivery)
			if disposition != tc.want || code != "http_"+strconv.Itoa(tc.status) {
				t.Fatalf("status %d = disposition:%d code:%q", tc.status, disposition, code)
			}
			if err == nil || strings.Contains(err.Error(), "private-response-body") {
				t.Fatalf("status %d error = %q", tc.status, err)
			}
		})
	}
}

func TestDeliverAuditDoesNotCallWithoutAuthority(t *testing.T) {
	delivery, _, _ := auditDeliveryFixture(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	t.Setenv("MS_DISPATCH_URL", server.URL)
	t.Setenv("MS_INTERNAL_SECRET", "")

	disposition, code, err := deliverAudit(context.Background(), delivery)
	if disposition != auditRetry || code != "authority_unavailable" || err == nil || calls.Load() != 0 {
		t.Fatalf("deliver = disposition:%d code:%q err:%v calls:%d", disposition, code, err, calls.Load())
	}
}

func TestAuditRetryBackoffIsBounded(t *testing.T) {
	if got := auditRetryBackoff(1); got != 2*time.Second {
		t.Fatalf("first backoff = %s", got)
	}
	if got := auditRetryBackoff(1_000); got > auditMaximumBackoff {
		t.Fatalf("backoff = %s, max %s", got, auditMaximumBackoff)
	}
}
