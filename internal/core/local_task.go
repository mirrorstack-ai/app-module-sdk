package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

const (
	localTaskQueued    = "queued"
	localTaskRunning   = "running"
	localTaskSucceeded = "succeeded"
	localTaskFailed    = "failed"
	localTaskCancelled = "cancelled"
)

// localTaskRegistry is the dev executor's process-local job store. A job is
// inserted before its goroutine is started, making idempotency and every state
// transition atomic even when callers enqueue, inspect, and cancel in parallel.
type localTaskRegistry struct {
	mu   sync.RWMutex
	jobs map[localTaskKey]*localTaskJob
}

type localTaskKey struct {
	appRef string
	id     string
}

type localTaskJob struct {
	id      string
	appRef  string
	name    string
	payload json.RawMessage
	status  string
	cancel  context.CancelFunc
}

func newLocalTaskRegistry() *localTaskRegistry {
	return &localTaskRegistry{jobs: make(map[localTaskKey]*localTaskJob)}
}

func (r *localTaskRegistry) enqueue(appRef, id, name string, payload json.RawMessage) (*localTaskJob, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := localTaskKey{appRef: appRef, id: id}
	if existing, ok := r.jobs[key]; ok {
		if existing.name != name || !bytes.Equal(existing.payload, payload) {
			return nil, false, fmt.Errorf("mirrorstack: local task idempotency conflict for job %q", id)
		}
		return existing, false, nil
	}

	job := &localTaskJob{
		id:      id,
		appRef:  appRef,
		name:    name,
		payload: append(json.RawMessage(nil), payload...),
		status:  localTaskQueued,
	}
	r.jobs[key] = job
	return job, true, nil
}

func (r *localTaskRegistry) start(key localTaskKey, cancel context.CancelFunc) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[key]
	if !ok || job.status != localTaskQueued {
		return false
	}
	job.status = localTaskRunning
	job.cancel = cancel
	return true
}

func (r *localTaskRegistry) finish(key localTaskKey, status string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[key]
	if !ok || job.status != localTaskRunning {
		return
	}
	job.status = status
	job.cancel = nil
}

func (r *localTaskRegistry) status(appRef, id string) (TaskJobStatus, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	job, ok := r.jobs[localTaskKey{appRef: appRef, id: id}]
	if !ok {
		return TaskJobStatus{}, fmt.Errorf("mirrorstack: local task job %q not found", id)
	}
	return TaskJobStatus{
		JobID:     job.id,
		Status:    job.status,
		Cancelled: job.status == localTaskCancelled,
	}, nil
}

func (r *localTaskRegistry) cancelJob(appRef, id string) error {
	r.mu.Lock()
	job, ok := r.jobs[localTaskKey{appRef: appRef, id: id}]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("mirrorstack: local task job %q not found", id)
	}
	var cancel context.CancelFunc
	switch job.status {
	case localTaskQueued, localTaskRunning:
		job.status = localTaskCancelled
		cancel = job.cancel
		job.cancel = nil
	}
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	return nil
}

func (m *Module) enqueueLocalTask(ctx context.Context, name string, payload json.RawMessage, idempotencyKey string, entry taskEntry) (string, error) {
	appRef, _ := managedTaskAppRef(ctx)
	job, created, err := m.localTasks.enqueue(appRef, idempotencyKey, name, payload)
	if err != nil {
		return "", err
	}
	if !created {
		return job.id, nil
	}

	// WithoutCancel preserves request-scoped values while detaching execution
	// from the dependency HTTP request's cancellation and deadline.
	workerBase := context.WithoutCancel(ctx)
	go m.executeLocalTask(workerBase, job, entry)
	return job.id, nil
}

func (m *Module) executeLocalTask(base context.Context, job *localTaskJob, entry taskEntry) {
	key := localTaskKey{appRef: job.appRef, id: job.id}
	jobCtx, jobCancel := context.WithCancel(base)
	defer jobCancel()

	ctx := jobCtx
	if entry.timeout > 0 {
		var timeoutCancel context.CancelFunc
		ctx, timeoutCancel = context.WithTimeout(ctx, entry.timeout)
		defer timeoutCancel()
	}

	if !m.localTasks.start(key, jobCancel) {
		return
	}

	status := localTaskSucceeded
	func() {
		defer func() {
			if recover() != nil {
				status = localTaskFailed
			}
		}()
		err := entry.handler(ctx, append(json.RawMessage(nil), job.payload...))
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			status = localTaskFailed
		case ctx.Err() != nil:
			status = localTaskCancelled
		case err != nil:
			status = localTaskFailed
		}
	}()
	m.localTasks.finish(key, status)
}
