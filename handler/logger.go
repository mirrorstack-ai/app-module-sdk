package handler

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"
)

var statusWriterPool = sync.Pool{New: func() any { return &statusWriter{} }}

// InitLogger configures the default slog logger.
// Lambda (AWS_LAMBDA_FUNCTION_NAME set) → JSON output.
// Local dev → text output.
// LOG_LEVEL env var: debug, info (default), warn, error.
//
// Call once in main() before Start():
//
//	handler.InitLogger()
func InitLogger() {
	var h slog.Handler
	if os.Getenv("AWS_LAMBDA_FUNCTION_NAME") != "" {
		h = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel()})
	} else {
		h = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel()})
	}
	slog.SetDefault(slog.New(h))
}

func logLevel() slog.Level {
	switch os.Getenv("LOG_LEVEL") {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// RequestLogger is middleware that logs every request with context fields
// (request_id, app_id, module_id) and injects a contextual slog.Logger.
//
// Must be placed after ExtractContext in the middleware chain.
//
//	r.Use(handler.ExtractContext)
//	r.Use(handler.RequestLogger)
//
// Then in handlers:
//
//	handler.Logger(ctx).Info("video uploaded", "videoId", id)
func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ctx := r.Context()

		logger := slog.Default().With(
			slog.String("request_id", GetRequestID(ctx)),
			slog.String("app_id", GetAppID(ctx)),
			slog.String("module_id", GetModuleID(ctx)),
		)

		ctx = context.WithValue(ctx, ctxLogger, logger)

		sw := statusWriterPool.Get().(*statusWriter)
		sw.ResponseWriter = w
		sw.status = http.StatusOK
		sw.wroteHeader = false
		defer statusWriterPool.Put(sw)

		next.ServeHTTP(sw, r.WithContext(ctx))

		duration := time.Since(start)
		logger.LogAttrs(ctx, levelForStatus(sw.status), "request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", sw.status),
			slog.Duration("duration", duration),
		)
	})
}

// Logger retrieves the contextual logger from the request context.
// Falls back to slog.Default() if RequestLogger middleware was not applied.
//
//	handler.Logger(ctx).Info("video uploaded", "videoId", id)
func Logger(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxLogger).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

// statusWriter wraps ResponseWriter to capture the status code.
type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.wroteHeader = true
	}
	return w.ResponseWriter.Write(b)
}

func levelForStatus(status int) slog.Level {
	switch {
	case status >= 500:
		return slog.LevelError
	case status >= 400:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}
