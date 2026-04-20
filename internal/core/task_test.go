package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

	var received json.RawMessage
	m.OnTask("echo", func(ctx context.Context, payload json.RawMessage) error {
		received = payload
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
	if string(received) != `{"key":"value"}` {
		t.Errorf("received = %s, want {\"key\":\"value\"}", string(received))
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

func TestRunTask_DevMode_PropagatesError(t *testing.T) {
	m := newTestModuleWithSecret(t, "test")
	m.OnTask("fail", func(ctx context.Context, p json.RawMessage) error {
		return context.DeadlineExceeded
	})

	_, err := m.RunTask(context.Background(), "fail", json.RawMessage(`{}`))
	if err == nil {
		t.Error("RunTask should propagate handler error in dev mode")
	}
}
