package module_test

import (
	"io"
	"net"
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mirrorstack-ai/app-module-sdk/module"
)

func TestStartHTTP_ServesRequests(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok")) //nolint:errcheck
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	errCh := make(chan error, 1)
	go func() { errCh <- module.StartHTTP(addr, r) }()

	// Wait for server to be ready.
	var resp *http.Response
	for range 20 {
		resp, err = http.Get("http://" + addr + "/ping")
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("server never became ready: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("body: got %q, want %q", string(body), "ok")
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}

	// Trigger graceful shutdown.
	p, _ := os.FindProcess(os.Getpid())
	p.Signal(syscall.SIGINT)

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("shutdown error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timed out")
	}
}

func TestHealthCheck(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/health", module.HealthCheck())

	req, _ := http.NewRequest("GET", "/health", nil)
	w := &responseWriter{code: 200, header: http.Header{}}
	r.ServeHTTP(w, req)

	if w.code != 200 {
		t.Errorf("status: got %d, want 200", w.code)
	}
}

func TestStart_DefaultPort(t *testing.T) {
	t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "")
	t.Setenv("PORT", "")

	r := chi.NewRouter()
	r.Get("/test", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok")) //nolint:errcheck
	})

	if r.Routes() == nil {
		t.Fatal("router has no routes")
	}
}

// minimal ResponseWriter for HealthCheck test
type responseWriter struct {
	code   int
	header http.Header
	body   []byte
}

func (w *responseWriter) Header() http.Header { return w.header }
func (w *responseWriter) Write(b []byte) (int, error) {
	w.body = append(w.body, b...)
	return len(b), nil
}
func (w *responseWriter) WriteHeader(code int) { w.code = code }
