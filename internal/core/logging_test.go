package core

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/lambdacontext"
	"github.com/mirrorstack-ai/app-module-sdk/auth"
)

func TestParseLogLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":    slog.LevelDebug,
		"info":     slog.LevelInfo,
		"":         slog.LevelInfo,
		"nonsense": slog.LevelInfo,
		"WARN":     slog.LevelWarn,
		"warning":  slog.LevelWarn,
		" error ":  slog.LevelError,
	}
	for in, want := range cases {
		if got := parseLogLevel(in); got != want {
			t.Errorf("parseLogLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestRequestID_PrefersAwsRequestID(t *testing.T) {
	ctx := lambdacontext.NewContext(context.Background(), &lambdacontext.LambdaContext{AwsRequestID: "aws-req-1"})
	if got := requestID(ctx); got != "aws-req-1" {
		t.Errorf("requestID with lambda ctx = %q, want aws-req-1", got)
	}
	if got := requestID(context.Background()); got == "" {
		t.Error("requestID without lambda ctx should mint a non-empty id")
	}
}

func TestLoggerFrom_DefaultOutsideRequest(t *testing.T) {
	if LoggerFrom(context.Background()) == nil {
		t.Error("LoggerFrom must never return nil")
	}
}

func TestRequestLogMiddleware_TagsCorrelation(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	m := &Module{config: Config{ID: "oauthcore"}}
	ran := false
	h := m.requestLogMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		LoggerFrom(r.Context()).Info("hello")
		ran = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(auth.Set(req.Context(), auth.Identity{AppID: "app-1", UserID: "user-1", AppRole: "admin"}))
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !ran {
		t.Fatal("handler did not run")
	}
	out := buf.String()
	for _, want := range []string{
		`"module_id":"oauthcore"`,
		`"app_id":"app-1"`,
		`"user_id":"user-1"`,
		`"app_role":"admin"`,
		`"request_id":`,
		`"msg":"hello"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log line missing %s\ngot: %s", want, out)
		}
	}
}

func TestRequestLogMiddleware_EmitsAccessLine(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	m := &Module{config: Config{ID: "oauthcore"}}
	// A handler that sets a non-200 status and never calls ms.Log: the access
	// line must still appear, with the status the handler wrote.
	h := m.requestLogMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))

	req := httptest.NewRequest(http.MethodGet, "/public/start?state=secret", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	out := buf.String()
	for _, want := range []string{
		`"msg":"request"`,
		`"method":"GET"`,
		`"path":"/public/start"`,
		`"status":403`,
		`"duration_ms":`,
		`"module_id":"oauthcore"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("access line missing %s\ngot: %s", want, out)
		}
	}
	// the query string (which can carry tokens) must never be logged
	if strings.Contains(out, "state=secret") {
		t.Errorf("access line leaked query string\ngot: %s", out)
	}
}
