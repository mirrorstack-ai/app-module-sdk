package mirrorstack

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/cache"
	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/httputil"
	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
	"github.com/mirrorstack-ai/app-module-sdk/internal/runtime"
	"github.com/mirrorstack-ai/app-module-sdk/storage"
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

// maxTaskPayloadSize is the SQS message body limit. RunTask validates payload
// size before sending so the error is clear, not a cryptic SQS SDK error.
const maxTaskPayloadSize = 256 * 1024 // 256 KB

// RunTask dispatches a background task to the module's SQS queue. Returns
// the generated TaskID (UUID). In dev mode (MS_TASK_QUEUE_URL unset),
// dispatches in-process to the registered OnTask handler.
//
// ctx must carry the same credentials as a Lambda handler — RunTask copies
// them into the TaskMessage so the task worker has the same DB/Cache/Storage
// access as the caller.
//
// WARNING: If called inside ms.Tx(), the SQS send is NOT transactional with
// the database write. The task may execute before, after, or even if the
// transaction rolls back. Use the transactional outbox pattern (future)
// for atomic dispatch.
//
//	taskID, err := mod.RunTask(r.Context(), "transcode-video", payload)
func (m *Module) RunTask(ctx context.Context, name string, payload json.RawMessage) (string, error) {
	if _, ok := m.taskHandlers[name]; !ok {
		return "", fmt.Errorf("mirrorstack: RunTask(%q): unknown task (not registered via OnTask)", name)
	}

	if len(payload) > maxTaskPayloadSize {
		return "", fmt.Errorf("mirrorstack: RunTask(%q): payload size %d exceeds SQS limit of %d bytes", name, len(payload), maxTaskPayloadSize)
	}

	msg := m.buildTaskMessage(ctx, name, payload)

	// Dev mode: dispatch in-process when no queue is configured.
	if m.sqsClient == nil {
		entry := m.taskHandlers[name]
		return msg.TaskID, entry.handler(ctx, payload)
	}

	// Sign the message for integrity verification by the task worker.
	msg.Sign(m.signingKey)

	body, err := json.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("mirrorstack: RunTask(%q): marshal: %w", name, err)
	}

	if _, err := m.sqsClient.Send(ctx, string(body)); err != nil {
		return "", fmt.Errorf("mirrorstack: RunTask(%q): %w", name, err)
	}
	return msg.TaskID, nil
}

// buildTaskMessage constructs a TaskMessage from the current request context,
// copying credentials so the task worker has identical DB/Cache/Storage access.
func (m *Module) buildTaskMessage(ctx context.Context, name string, payload json.RawMessage) *runtime.TaskMessage {
	msg := &runtime.TaskMessage{
		TaskID:  generateTaskID(),
		Name:    name,
		Payload: payload,
	}

	// Copy credentials from context — these were injected by the Lambda handler.
	var res runtime.Resources
	if cred := db.CredentialFrom(ctx); cred != nil {
		res.DB = cred
	}
	if cred := cache.CredentialFrom(ctx); cred != nil {
		res.Cache = cred
	}
	if cred := storage.CredentialFrom(ctx); cred != nil {
		res.Storage = cred
	}
	if res.DB != nil || res.Cache != nil || res.Storage != nil {
		msg.Resources = &res
	}

	if a := auth.Get(ctx); a != nil {
		msg.UserID = a.UserID
		msg.AppID = a.AppID
		msg.AppRole = a.AppRole
	}

	msg.AppSchema = db.SchemaFrom(ctx)

	return msg
}

// generateTaskID returns a new UUID v4 string for task deduplication.
func generateTaskID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("mirrorstack: crypto/rand.Read failed: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 2
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// OnTask registers a task handler on the default Module created by Init().
// Panics before Init — matches Platform/Public/Internal.
func OnTask(name string, handler TaskHandler, opts ...TaskOption) {
	mustDefault("OnTask").OnTask(name, handler, opts...)
}

// RunTask dispatches a task on the default Module. Panics before Init.
func RunTask(ctx context.Context, name string, payload json.RawMessage) (string, error) {
	return mustDefault("RunTask").RunTask(ctx, name, payload)
}
