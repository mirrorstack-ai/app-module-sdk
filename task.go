package mirrorstack

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mirrorstack-ai/app-module-sdk/internal/httputil"
	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
)

// TaskHandler is the handler function for background tasks dispatched via SQS.
// ctx carries the same credentials as a Lambda handler (DB, Cache, Storage,
// Auth identity). Returning nil acks the message; returning an error leaves it
// in the queue for retry.
type TaskHandler func(ctx context.Context, payload json.RawMessage) error

// TaskOption configures task registration. See WithTimeout.
type TaskOption func(*taskOptions)

type taskOptions struct {
	timeout    time.Duration
	maxRetries int
}

// taskEntry stores the handler and its configured options.
type taskEntry struct {
	handler TaskHandler
	timeout time.Duration
}

// WithTimeout sets the maximum duration for this task handler. The worker wraps
// invocations in context.WithTimeout. Also exposed in the manifest so the
// platform can set the SQS visibility timeout appropriately.
func WithTimeout(d time.Duration) TaskOption {
	return func(o *taskOptions) { o.timeout = d }
}

// WithMaxRetries sets the maximum number of SQS retries before routing to DLQ.
// Exposed in the manifest so the platform can configure the redrive policy.
func WithMaxRetries(n int) TaskOption {
	return func(o *taskOptions) { o.maxRetries = n }
}

const taskPathPrefix = "/__mirrorstack/tasks/"

// OnTask registers a background task handler. The handler appears in the
// manifest so the platform can provision SQS queues on deploy. A dev HTTP
// endpoint is mounted at POST /__mirrorstack/tasks/{name} on the Internal
// scope for curl testing.
//
// Names must not contain path separators (/, \), whitespace, dot-segments
// (..), or null bytes. Call from startup code (init / main), not from inside
// a request handler.
//
// Panics on duplicate registration with the same name.
//
//	mod.OnTask("transcode-video", transcodeHandler, ms.WithTimeout(10*time.Minute))
func (m *Module) OnTask(name string, handler TaskHandler, opts ...TaskOption) {
	o := taskOptions{}
	for _, opt := range opts {
		opt(&o)
	}

	task := registry.Task{Name: name}
	if o.timeout > 0 {
		task.MaxDuration = o.timeout.String()
	}
	if o.maxRetries > 0 {
		task.MaxRetries = o.maxRetries
	}

	if !m.registry.AddTask(task) {
		panic("mirrorstack: OnTask(" + name + ") registered twice")
	}

	m.taskHandlers[name] = taskEntry{
		handler: handler,
		timeout: o.timeout,
	}

	// Mount a dev/debug HTTP endpoint on the Internal scope so developers
	// can test task handlers via curl without SQS infrastructure.
	path := taskPathPrefix + name
	m.Internal(func(r chi.Router) {
		r.Post(path, m.taskHTTPHandler(name))
	})
}

// taskHTTPHandler returns an http.HandlerFunc that dispatches to the named
// task handler. Used for the dev HTTP endpoint only — production tasks arrive
// via SQS, not HTTP.
func (m *Module) taskHTTPHandler(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		entry, ok := m.taskHandlers[name]
		if !ok {
			httputil.JSON(w, http.StatusNotFound, httputil.ErrorResponse{Error: "unknown task"})
			return
		}

		var payload json.RawMessage
		if r.Body != nil {
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				httputil.JSON(w, http.StatusBadRequest, httputil.ErrorResponse{Error: "invalid request body: " + err.Error()})
				return
			}
		}

		ctx := r.Context()
		if entry.timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, entry.timeout)
			defer cancel()
		}

		if err := entry.handler(ctx, payload); err != nil {
			httputil.JSON(w, http.StatusInternalServerError, httputil.ErrorResponse{Error: err.Error()})
			return
		}
		httputil.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// OnTask registers a task handler on the default Module created by Init().
// Panics before Init — matches Platform/Public/Internal.
func OnTask(name string, handler TaskHandler, opts ...TaskOption) {
	mustDefault("OnTask").OnTask(name, handler, opts...)
}
