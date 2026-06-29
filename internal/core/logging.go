package core

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/aws/aws-lambda-go/lambdacontext"
	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/internal/ids"
)

type logCtxKey struct{}

// configureLogging installs a JSON slog handler on stdout as the process
// default. Module logs (via ms.Log) and any slog call then emit one structured
// JSON line per event, captured identically by CloudWatch (prod Lambda) and the
// `mirrorstack dev` runner (dev) — so the platform Logcat reads one clean stream
// with no transport code. Level is MS_LOG_LEVEL (debug|info|warn|error), default
// info. The SDK's own stderr diagnostic logger (m.logger) is left as-is.
func configureLogging() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLogLevel(os.Getenv("MS_LOG_LEVEL")),
	})))
}

func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// requestLogMiddleware attaches a per-request *slog.Logger to the context,
// pre-tagged with the trusted correlation fields so every ms.Log(ctx) line is
// attributable in the platform Logcat. It runs on BOTH serving paths (it only
// READS context): identity is already established upstream — the Lambda shim's
// InjectResources in prod, devAppSchemaMiddleware in dev — and app id is the
// trusted, unspoofable value (never a request field).
func (m *Module) requestLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := slog.With("module_id", m.config.ID, "request_id", requestID(ctx))
		if id := auth.Get(ctx); id != nil {
			logger = logger.With("app_id", id.AppID, "user_id", id.UserID, "app_role", id.AppRole)
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, logCtxKey{}, logger)))
	})
}

// requestID is the prod Lambda request id when present (so the module line shares
// CloudWatch's own request grouping), else a fresh dev id.
func requestID(ctx context.Context) string {
	if lc, ok := lambdacontext.FromContext(ctx); ok && lc.AwsRequestID != "" {
		return lc.AwsRequestID
	}
	return ids.NewUUID()
}

// LoggerFrom returns the per-request logger attached by requestLogMiddleware, or
// the process default if called outside a request. Exposed as ms.Log.
func LoggerFrom(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(logCtxKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}
