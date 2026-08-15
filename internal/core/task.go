package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/internal/httputil"
	"github.com/mirrorstack-ai/app-module-sdk/internal/ids"
	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
	"github.com/mirrorstack-ai/app-module-sdk/internal/runtime"
)

// TaskHandler handles one platform-managed background task attempt. Returning
// an ordinary error requests a retry according to the declared retry policy.
type TaskHandler func(ctx context.Context, payload json.RawMessage) error

// PermanentTaskError marks a handler failure as non-retryable while preserving
// errors.Is/errors.As through Unwrap.
type PermanentTaskError struct{ Err error }

func (e *PermanentTaskError) Error() string {
	if e == nil || e.Err == nil {
		return "permanent task failure"
	}
	return e.Err.Error()
}
func (e *PermanentTaskError) Unwrap() error   { return e.Err }
func (e *PermanentTaskError) Retryable() bool { return false }

// Permanent marks err as a non-retryable task failure. A nil error stays nil.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &PermanentTaskError{Err: err}
}

// IsPermanent reports whether err contains a PermanentTaskError wrapper.
func IsPermanent(err error) bool {
	var target *PermanentTaskError
	return errors.As(err, &target)
}

// TaskOption configures task registration. See WithTimeout.
type TaskOption func(*taskOptions)

type taskOptions struct {
	timeout          time.Duration
	maxRetries       int
	compute          *Compute
	ephemeralStorage *int64
}

// taskEntry stores the handler and its configured options.
type taskEntry struct {
	handler TaskHandler
	timeout time.Duration
}

// WithTimeout sets the maximum duration for this task handler.
func WithTimeout(d time.Duration) TaskOption {
	return func(o *taskOptions) { o.timeout = d }
}

// WithMaxRetries sets the maximum number of platform-managed retry attempts.
func WithMaxRetries(n int) TaskOption {
	return func(o *taskOptions) { o.maxRetries = n }
}

// Binary size constants accepted by WithEphemeralStorage.
const (
	MiB int64 = 1024 * 1024
	GiB       = 1024 * MiB
)

// WithEphemeralStorage declares Heavy/Fargate runner-local scratch storage.
// Heavy tasks accept 20 GiB (the implicit default, omitted from the manifest)
// or a whole-GiB value from 21 through 200 GiB. Standard/Lambda and GPU tasks
// do not expose this option; GPU host scratch is platform-owned and fixed.
func WithEphemeralStorage(sizeBytes int64) TaskOption {
	return func(o *taskOptions) { o.ephemeralStorage = &sizeBytes }
}

// WithCompute selects the execution class and size for this task, so the
// platform provisions the right managed runner from the declaration alone.
//
// Omitting it keeps the task on Standard/Lambda with platform-default sizing:
// the default must never move, or a module could land on expensive compute
// without anyone declaring it.
//
// The descriptor must come from Standard, Heavy or GPU — those validate it.
// A zero Compute is a programmer error and panics rather than reaching the
// manifest as a runner class the platform cannot resolve.
func WithCompute(c Compute) TaskOption {
	if c.class == "" {
		panic("mirrorstack: WithCompute(Compute{}) — an empty descriptor cannot select a runner; build one with ms.Standard, ms.Heavy or ms.GPU")
	}
	return func(o *taskOptions) { o.compute = &c }
}

const taskPathPrefix = "/__mirrorstack/tasks/"

// OnTask registers a background task handler. The handler appears in the
// manifest so the platform can admit managed task jobs. A dev HTTP
// endpoint is mounted at POST /__mirrorstack/tasks/{name} on the Internal
// scope for curl testing.
//
// Names must not contain path separators (/, \), whitespace, dot-segments
// (..), or null bytes. Call from startup code (init / main), not from inside
// a request handler.
//
// Panics on duplicate registration with the same name.
//
// The execution class defaults to Standard — the module's own Lambda, with
// Lambda's 15-minute ceiling. Pass WithCompute to route the task to a heavier
// runner instead; the platform provisions it from the manifest at deploy.
//
//	mod.OnTask("thumbnail", thumbnailHandler, ms.WithTimeout(10*time.Minute))
//
//	mod.OnTask("transcode-video", transcodeHandler,
//	    ms.WithCompute(ms.Heavy(ms.Res{VCPU: 4, MemoryMB: 8192})),
//	    ms.WithTimeout(90*time.Minute),
//	    ms.WithMaxRetries(2),
//	)
func (m *Module) OnTask(name string, handler TaskHandler, opts ...TaskOption) {
	o := taskOptions{}
	for _, opt := range opts {
		opt(&o)
	}
	if handler == nil {
		panic("mirrorstack: OnTask(" + name + "): handler must not be nil")
	}
	if len(name) > 128 {
		panic("mirrorstack: OnTask(" + name + "): task name cannot exceed 128 bytes")
	}
	if o.timeout < 0 {
		panic("mirrorstack: OnTask(" + name + "): timeout must not be negative")
	}
	if o.timeout%time.Second != 0 {
		panic("mirrorstack: OnTask(" + name + "): timeout must be a whole number of seconds")
	}
	if o.maxRetries < 0 || o.maxRetries > 10 {
		panic("mirrorstack: OnTask(" + name + "): max retries must be between 0 and 10")
	}
	class := classStandard
	if o.compute != nil {
		class = o.compute.class
	}
	if class == classStandard && o.timeout > 15*time.Minute {
		panic("mirrorstack: OnTask(" + name + "): Standard tasks cannot exceed Lambda's 15-minute limit")
	}
	if class != classStandard && o.timeout > 24*time.Hour {
		panic("mirrorstack: OnTask(" + name + "): Heavy and GPU tasks cannot exceed the 24-hour managed-task limit")
	}

	task := registry.Task{Name: name}
	if o.timeout > 0 {
		task.MaxDuration = o.timeout.String()
	}
	if o.maxRetries > 0 {
		task.MaxRetries = o.maxRetries
	}
	// Only a declared class is projected: an undeclared one leaves the manifest
	// entry byte-identical to what it was before this field existed.
	if o.compute != nil {
		task.Compute = o.compute.taskCompute()
	}
	if o.ephemeralStorage != nil {
		const defaultBytes = 20 * GiB
		size := *o.ephemeralStorage
		if size%GiB != 0 {
			panic("mirrorstack: OnTask(" + name + "): ephemeral storage must be aligned to a whole GiB")
		}
		switch class {
		case classHeavy:
			if size < defaultBytes || size > 200*GiB || (size > defaultBytes && size < 21*GiB) {
				panic("mirrorstack: OnTask(" + name + "): Heavy ephemeral storage must be the 20 GiB default or between 21 and 200 GiB")
			}
			if size != defaultBytes {
				task.EphemeralStorageMB = int(size / MiB)
			}
		default:
			panic("mirrorstack: OnTask(" + name + "): ephemeral storage is supported only for Heavy tasks")
		}
	}

	if !m.registry.AddTask(task) {
		panic("mirrorstack: OnTask(" + name + ") registered twice")
	}

	m.taskHandlers[name] = taskEntry{
		handler: handler,
		timeout: o.timeout,
	}

	// Mount a dev/debug HTTP endpoint on the Internal scope so developers can
	// test task handlers via curl without the managed task plane.
	path := taskPathPrefix + name
	m.mountSystemInternalRoute("POST", path, m.taskHTTPHandler(name))
}

// taskHTTPHandler returns an http.HandlerFunc that dispatches to the named
// task handler. Used for the dev HTTP endpoint only.
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

// maxTaskPayloadSize retains the SDK's existing public payload ceiling.
const maxTaskPayloadSize = 256 * 1024 // 256 KB

// RunTask creates a task job and returns its job ID. Deployed requests contain
// only the caller's raw JSON payload; execution identity, resources, compute
// and artifacts are resolved by the platform. CLI dev mode instead enqueues
// the registered handler asynchronously in a process-local executor.
//
//	taskID, err := mod.RunTask(r.Context(), "transcode-video", payload)
func (m *Module) RunTask(ctx context.Context, name string, payload json.RawMessage) (string, error) {
	return m.runTask(ctx, name, payload, ids.NewUUID())
}

// RunTaskWithIdempotencyKey creates or recovers one logical task job. Persist
// the UUID with the caller's logical work before enqueueing, then reuse it after
// ambiguous failures or process restarts. Both the local executor and managed
// platform return the original job for an exact retry and reject key reuse with
// a different task or payload.
func (m *Module) RunTaskWithIdempotencyKey(ctx context.Context, name string, payload json.RawMessage, idempotencyKey string) (string, error) {
	if !uuidPattern.MatchString(idempotencyKey) {
		return "", errors.New("mirrorstack: RunTaskWithIdempotencyKey requires a UUID idempotency key")
	}
	return m.runTask(ctx, name, payload, idempotencyKey)
}

func (m *Module) runTask(ctx context.Context, name string, payload json.RawMessage, idempotencyKey string) (string, error) {
	if _, ok := m.taskHandlers[name]; !ok {
		return "", fmt.Errorf("mirrorstack: RunTask(%q): unknown task (not registered via OnTask)", name)
	}

	canonicalPayload, err := canonicalTaskPayload(payload)
	if err != nil {
		return "", fmt.Errorf("mirrorstack: RunTask(%q): payload is not valid JSON", name)
	}
	if len(canonicalPayload) > maxTaskPayloadSize {
		return "", fmt.Errorf("mirrorstack: RunTask(%q): payload size %d exceeds limit of %d bytes", name, len(canonicalPayload), maxTaskPayloadSize)
	}
	if m.devMode {
		entry := m.taskHandlers[name]
		return m.enqueueLocalTask(ctx, name, canonicalPayload, idempotencyKey, entry)
	}
	return m.runManagedTask(ctx, name, canonicalPayload, idempotencyKey)
}

// canonicalTaskPayload mirrors the managed task plane's idempotency boundary:
// insignificant JSON formatting and object-key order do not create a distinct
// logical request. An omitted SDK payload remains the historical JSON null.
func canonicalTaskPayload(payload json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(payload)) == 0 {
		return json.RawMessage("null"), nil
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("multiple JSON values")
	}
	return json.Marshal(value)
}

const managedTaskResponseLimit = 8 << 10

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type managedTaskResponse struct {
	Version      int    `json:"v"`
	JobID        string `json:"jobId"`
	Status       string `json:"status"`
	Deduplicated bool   `json:"deduplicated"`
}

// TaskJobStatus is the current state of a task job. In CLI dev mode it comes
// from the process-local executor; deployed calls read platform-owned state.
type TaskJobStatus struct {
	JobID     string `json:"jobId"`
	Status    string `json:"status"`
	Cancelled bool   `json:"cancelled"`
}

type managedTaskJobResponse struct {
	Version int `json:"v"`
	TaskJobStatus
}

var managedTaskStatuses = map[string]struct{}{
	"queued": {}, "running": {}, "succeeded": {}, "failed": {}, "cancelled": {},
}

func managedTaskBase(raw string) (string, error) {
	base := strings.TrimSpace(raw)
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid managed task URL")
	}
	return strings.TrimRight(base, "/"), nil
}

func managedTaskDispatchBase(ctx context.Context) string {
	if base := runtime.TaskDispatchURLFrom(ctx); base != "" {
		return base
	}
	return os.Getenv("MS_DISPATCH_URL")
}

// TaskStatus reads process-local state in CLI dev mode. Deployed jobs are
// scoped to the caller's app and this module; a sibling module cannot inspect
// them by guessing an ID.
func (m *Module) TaskStatus(ctx context.Context, jobID string) (TaskJobStatus, error) {
	var zero TaskJobStatus
	if !uuidPattern.MatchString(jobID) {
		return zero, errors.New("mirrorstack: TaskStatus requires a UUID job ID")
	}
	if m.devMode {
		appRef, _ := managedTaskAppRef(ctx)
		return m.localTasks.status(appRef, jobID)
	}
	endpoint, secret, err := m.managedTaskJobEndpoint(ctx, jobID, "TaskStatus")
	if err != nil {
		return zero, err
	}
	body, err := m.doManagedTaskRequest(ctx, http.MethodGet, endpoint, secret, "", nil, "TaskStatus")
	if err != nil {
		return zero, err
	}
	return decodeManagedTaskJob(body, jobID, "TaskStatus")
}

// CancelTask requests monotonic cancellation of a local or managed job.
// Cancellation is idempotent for queued, running, already-cancelled, and
// terminal jobs.
func (m *Module) CancelTask(ctx context.Context, jobID string) error {
	if !uuidPattern.MatchString(jobID) {
		return errors.New("mirrorstack: CancelTask requires a UUID job ID")
	}
	if m.devMode {
		appRef, _ := managedTaskAppRef(ctx)
		return m.localTasks.cancelJob(appRef, jobID)
	}
	endpoint, secret, err := m.managedTaskJobEndpoint(ctx, jobID, "CancelTask")
	if err != nil {
		return err
	}
	endpoint += "/cancel"
	body, err := m.doManagedTaskRequest(ctx, http.MethodPost, endpoint, secret, ids.NewUUID(), nil, "CancelTask")
	if err != nil {
		return err
	}
	_, err = decodeManagedTaskJob(body, jobID, "CancelTask")
	return err
}

func decodeManagedTaskJob(body []byte, jobID, operation string) (TaskJobStatus, error) {
	var result managedTaskJobResponse
	if err := json.Unmarshal(body, &result); err != nil || result.Version != 1 || result.JobID != jobID {
		return TaskJobStatus{}, fmt.Errorf("mirrorstack: %s: malformed managed task response", operation)
	}
	if _, ok := managedTaskStatuses[result.Status]; !ok {
		return TaskJobStatus{}, fmt.Errorf("mirrorstack: %s: managed task response has unknown status", operation)
	}
	return result.TaskJobStatus, nil
}

func (m *Module) managedTaskJobEndpoint(ctx context.Context, jobID, operation string) (string, string, error) {
	appRef, err := managedTaskAppRef(ctx)
	if err != nil {
		return "", "", fmt.Errorf("mirrorstack: %s requires an app-scoped context", operation)
	}
	base, err := managedTaskBase(managedTaskDispatchBase(ctx))
	if err != nil {
		return "", "", fmt.Errorf("mirrorstack: %s: valid MS_DISPATCH_URL is required", operation)
	}
	secret, err := outboundServiceSecret(ctx, operation)
	if err != nil {
		return "", "", err
	}
	if secret == "" {
		return "", "", fmt.Errorf("mirrorstack: %s: MS_INTERNAL_SECRET module credential is required", operation)
	}
	endpoint := base + "/internal/apps/" + url.PathEscape(appRef) +
		"/modules/" + url.PathEscape(m.config.ID) + "/task-jobs/" + url.PathEscape(jobID)
	return endpoint, secret, nil
}

func (m *Module) doManagedTaskRequest(ctx context.Context, method, endpoint, secret, idempotencyKey string, body []byte, operation string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(attempt) * 100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		var reader io.Reader = http.NoBody
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
		if err != nil {
			return nil, fmt.Errorf("mirrorstack: %s: build request: %w", operation, err)
		}
		req.Header.Set("X-MS-Service-Secret", secret)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if idempotencyKey != "" {
			req.Header.Set("Idempotency-Key", idempotencyKey)
		}
		resp, err := m.taskHTTPClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = fmt.Errorf("managed task transport failed: %w", err)
			continue
		}
		responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, managedTaskResponseLimit+1))
		resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("managed task response read failed: %w", readErr)
			continue
		}
		if len(responseBody) > managedTaskResponseLimit {
			return nil, fmt.Errorf("mirrorstack: %s: managed task response exceeds %d bytes", operation, managedTaskResponseLimit)
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("managed task endpoint returned HTTP %d", resp.StatusCode)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("mirrorstack: %s: managed task endpoint returned HTTP %d", operation, resp.StatusCode)
		}
		if mediaType := strings.ToLower(resp.Header.Get("Content-Type")); mediaType != "" && !strings.HasPrefix(mediaType, "application/json") {
			return nil, fmt.Errorf("mirrorstack: %s: managed task endpoint returned non-JSON content", operation)
		}
		return responseBody, nil
	}
	return nil, fmt.Errorf("mirrorstack: %s: %w", operation, lastErr)
}

func (m *Module) runManagedTask(ctx context.Context, name string, payload json.RawMessage, idempotencyKey string) (string, error) {
	appRef, err := managedTaskAppRef(ctx)
	if err != nil {
		return "", fmt.Errorf("mirrorstack: RunTask(%q) requires an app-scoped context", name)
	}
	rawBase := strings.TrimSpace(managedTaskDispatchBase(ctx))
	if rawBase == "" {
		return "", fmt.Errorf("mirrorstack: RunTask(%q): MS_DISPATCH_URL is required outside CLI dev mode", name)
	}
	base, err := managedTaskBase(rawBase)
	if err != nil {
		return "", fmt.Errorf("mirrorstack: RunTask(%q): MS_DISPATCH_URL is invalid", name)
	}
	secret, err := outboundServiceSecret(ctx, "RunTask")
	if err != nil {
		return "", err
	}
	if secret == "" {
		return "", fmt.Errorf("mirrorstack: RunTask(%q): MS_INTERNAL_SECRET module credential is required", name)
	}
	p := make([]byte, 0, len(payload)+12)
	p = append(p, `{"payload":`...)
	p = append(p, payload...)
	p = append(p, '}')
	endpoint := base + "/internal/apps/" + url.PathEscape(appRef) +
		"/modules/" + url.PathEscape(m.config.ID) + "/tasks/" + url.PathEscape(name)
	body, err := m.doManagedTaskRequest(ctx, http.MethodPost, endpoint, secret, idempotencyKey, p, "RunTask("+name+")")
	if err != nil {
		return "", err
	}
	var result managedTaskResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("mirrorstack: RunTask(%q): malformed managed enqueue response", name)
	}
	_, validStatus := managedTaskStatuses[result.Status]
	if result.Version != 1 || !validStatus || !uuidPattern.MatchString(result.JobID) {
		return "", fmt.Errorf("mirrorstack: RunTask(%q): malformed managed enqueue response", name)
	}
	return result.JobID, nil
}

func managedTaskAppRef(ctx context.Context) (string, error) {
	if appRef := runtime.TaskAppRefFrom(ctx); appRef != "" {
		return appRef, nil
	}
	if identity := auth.Get(ctx); identity != nil && identity.AppID != "" {
		return identity.AppID, nil
	}
	return "", errors.New("missing app scope")
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

// RunTaskWithIdempotencyKey dispatches a recoverable logical task on the
// default Module. Persist and reuse the UUID across ambiguous enqueue failures.
func RunTaskWithIdempotencyKey(ctx context.Context, name string, payload json.RawMessage, idempotencyKey string) (string, error) {
	return mustDefault("RunTaskWithIdempotencyKey").RunTaskWithIdempotencyKey(ctx, name, payload, idempotencyKey)
}

// TaskStatus returns a local or managed task's current state on the default Module.
func TaskStatus(ctx context.Context, jobID string) (TaskJobStatus, error) {
	return mustDefault("TaskStatus").TaskStatus(ctx, jobID)
}

// CancelTask requests local or managed cancellation on the default Module.
func CancelTask(ctx context.Context, jobID string) error {
	return mustDefault("CancelTask").CancelTask(ctx, jobID)
}
