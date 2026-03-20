package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/handler"
)

func TestRequestLogger_LogsRequest(t *testing.T) {
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))

	h := handler.ExtractContext(
		handler.RequestLogger(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}),
		),
	)

	r := httptest.NewRequest("GET", "/videos", nil)
	r.Header.Set(handler.HeaderRequestID, "req-123")
	r.Header.Set(handler.HeaderAppID, "app-456")
	r.Header.Set(handler.HeaderModuleID, "video")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	var logEntry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
		t.Fatalf("parse log: %v\nraw: %s", err, buf.String())
	}

	if logEntry["request_id"] != "req-123" {
		t.Errorf("request_id: got %v", logEntry["request_id"])
	}
	if logEntry["app_id"] != "app-456" {
		t.Errorf("app_id: got %v", logEntry["app_id"])
	}
	if logEntry["module_id"] != "video" {
		t.Errorf("module_id: got %v", logEntry["module_id"])
	}
	if logEntry["method"] != "GET" {
		t.Errorf("method: got %v", logEntry["method"])
	}
	if logEntry["path"] != "/videos" {
		t.Errorf("path: got %v", logEntry["path"])
	}
	if logEntry["status"] != float64(200) {
		t.Errorf("status: got %v", logEntry["status"])
	}
	if logEntry["duration"] == nil {
		t.Error("duration should be present")
	}
}

func TestRequestLogger_ErrorStatus_LogsWarn(t *testing.T) {
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))

	h := handler.ExtractContext(
		handler.RequestLogger(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			}),
		),
	)

	r := httptest.NewRequest("GET", "/missing", nil)
	h.ServeHTTP(httptest.NewRecorder(), r)

	var logEntry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
		t.Fatalf("parse log: %v", err)
	}

	if logEntry["level"] != "WARN" {
		t.Errorf("level: got %v, want WARN for 404", logEntry["level"])
	}
}

func TestRequestLogger_ServerError_LogsError(t *testing.T) {
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))

	h := handler.ExtractContext(
		handler.RequestLogger(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			}),
		),
	)

	r := httptest.NewRequest("GET", "/crash", nil)
	h.ServeHTTP(httptest.NewRecorder(), r)

	var logEntry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
		t.Fatalf("parse log: %v", err)
	}

	if logEntry["level"] != "ERROR" {
		t.Errorf("level: got %v, want ERROR for 500", logEntry["level"])
	}
}

func TestLogger_FromContext(t *testing.T) {
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))

	var captured *slog.Logger

	h := handler.ExtractContext(
		handler.RequestLogger(
			http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				captured = handler.Logger(r.Context())
				captured.Info("test message", "key", "value")
			}),
		),
	)

	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set(handler.HeaderRequestID, "req-ctx")
	h.ServeHTTP(httptest.NewRecorder(), r)

	if captured == nil {
		t.Fatal("logger should not be nil")
	}

	// Find the handler log line (not the request log).
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	var handlerLog map[string]any
	for _, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry["msg"] == "test message" {
			handlerLog = entry
			break
		}
	}

	if handlerLog == nil {
		t.Fatalf("handler log not found in output:\n%s", buf.String())
	}
	if handlerLog["request_id"] != "req-ctx" {
		t.Errorf("request_id: got %v", handlerLog["request_id"])
	}
	if handlerLog["key"] != "value" {
		t.Errorf("key: got %v", handlerLog["key"])
	}
}

func TestLogger_FallsBackToDefault(t *testing.T) {
	l := handler.Logger(context.Background())
	if l == nil {
		t.Fatal("should return default logger, not nil")
	}
}

func TestInitLogger_Lambda(t *testing.T) {
	t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "test-func")
	t.Setenv("LOG_LEVEL", "debug")
	handler.InitLogger()
	// Should not panic; verify by logging.
	slog.Info("test from lambda init")
}

func TestInitLogger_Local(t *testing.T) {
	t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "")
	t.Setenv("LOG_LEVEL", "warn")
	handler.InitLogger()
	slog.Info("test from local init")
}
