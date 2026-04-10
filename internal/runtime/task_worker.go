package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	msqs "github.com/mirrorstack-ai/app-module-sdk/internal/sqs"
)

// TaskHandlerFunc is the handler signature for dispatching tasks inside the
// worker loop. Matches the mirrorstack.TaskHandler type (which is in the
// parent package — we use a function type here to avoid an import cycle).
type TaskHandlerFunc func(ctx context.Context, payload json.RawMessage) error

// TaskEntry holds a registered task handler and its configured timeout.
type TaskEntry struct {
	Handler TaskHandlerFunc
	Timeout time.Duration
}

// WorkerConfig holds the configuration for a task worker poll loop.
type WorkerConfig struct {
	SQSClient    *msqs.Client
	Handlers     map[string]TaskEntry
	SigningKey   []byte
	Logger       *log.Logger
	IsProduction bool // true when MS_TASK_QUEUE_URL is set — rejects credential-missing messages
}

// PollLoop is the core worker loop: receive SQS messages, verify HMAC,
// dispatch to handlers, ack/nack. Exits when ctx is cancelled.
//
// Each message is processed sequentially within this goroutine. For
// concurrency, the caller spawns N goroutines each running PollLoop.
func PollLoop(ctx context.Context, cfg *WorkerConfig) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		msgs, err := cfg.SQSClient.Receive(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return // context cancelled during receive — expected shutdown
			}
			cfg.Logger.Printf("mirrorstack: sqs receive error: %v", err)
			backoff := time.NewTimer(5 * time.Second)
			select {
			case <-backoff.C:
			case <-ctx.Done():
				backoff.Stop()
				return
			}
			continue
		}

		for _, msg := range msgs {
			processMessage(ctx, cfg, msg)
		}
	}
}

func processMessage(ctx context.Context, cfg *WorkerConfig, msg msqs.Message) {
	var tm TaskMessage
	if err := json.Unmarshal([]byte(msg.Body), &tm); err != nil {
		cfg.Logger.Printf("mirrorstack: task message unmarshal error (messageId=%s): %v", msg.MessageID, err)
		// Malformed body — delete to prevent infinite retry
		cfg.SQSClient.Delete(ctx, msg.ReceiptHandle)
		return
	}

	// HMAC verification — reject forged messages
	if err := tm.Verify(cfg.SigningKey); err != nil {
		cfg.Logger.Printf("mirrorstack: task message rejected (taskId=%s name=%s): %v", tm.TaskID, tm.Name, err)
		// Invalid signature — delete to prevent retry (attacker message or key mismatch)
		cfg.SQSClient.Delete(ctx, msg.ReceiptHandle)
		return
	}

	// Lookup handler
	entry, ok := cfg.Handlers[tm.Name]
	if !ok {
		cfg.Logger.Printf("mirrorstack: unknown task %q (taskId=%s messageId=%s) — leaving for DLQ", tm.Name, tm.TaskID, msg.MessageID)
		// Don't delete — SQS visibility timeout will re-deliver, eventually DLQ
		return
	}

	// Production guard: reject credential-missing messages before injection
	// so we never silently fall through to the dev pool.
	if cfg.IsProduction && (tm.Resources == nil || tm.Resources.DB == nil) {
		cfg.Logger.Printf("mirrorstack: task missing credentials in production mode (taskId=%s name=%s) — deleting", tm.TaskID, tm.Name)
		cfg.SQSClient.Delete(ctx, msg.ReceiptHandle)
		return
	}

	// Inject credentials into context
	handlerCtx, err := InjectResources(ctx, InjectParams{
		Resources: tm.Resources,
		UserID:    tm.UserID,
		AppID:     tm.AppID,
		AppRole:   tm.AppRole,
		AppSchema: tm.AppSchema,
	})
	if err != nil {
		cfg.Logger.Printf("mirrorstack: task credential injection failed (taskId=%s name=%s): %v", tm.TaskID, tm.Name, err)
		// Validation error — retrying won't help, delete
		cfg.SQSClient.Delete(ctx, msg.ReceiptHandle)
		return
	}

	// Apply per-task timeout
	if entry.Timeout > 0 {
		var cancel context.CancelFunc
		handlerCtx, cancel = context.WithTimeout(handlerCtx, entry.Timeout)
		defer cancel()
	}

	// Spawn visibility heartbeat — extends the SQS visibility timeout while
	// the handler is running so long tasks don't get re-delivered.
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	go visibilityHeartbeat(heartbeatCtx, cfg, msg.ReceiptHandle, entry.Timeout)

	// Run handler with panic recovery
	cfg.Logger.Printf("mirrorstack: task started (taskId=%s name=%s receiveCount=%d)", tm.TaskID, tm.Name, msg.ReceiveCount)
	start := time.Now()
	handlerErr := safeRun(handlerCtx, entry.Handler, tm.Payload)
	duration := time.Since(start)
	stopHeartbeat()

	if handlerErr != nil {
		cfg.Logger.Printf("mirrorstack: task failed (taskId=%s name=%s duration=%s receiveCount=%d): %v", tm.TaskID, tm.Name, duration, msg.ReceiveCount, handlerErr)
		// Don't delete — SQS visibility timeout will re-deliver for retry
		return
	}

	cfg.Logger.Printf("mirrorstack: task completed (taskId=%s name=%s duration=%s)", tm.TaskID, tm.Name, duration)
	if err := cfg.SQSClient.Delete(ctx, msg.ReceiptHandle); err != nil {
		cfg.Logger.Printf("mirrorstack: sqs delete failed (taskId=%s): %v — message will be re-delivered (at-least-once)", tm.TaskID, err)
	}
}

// safeRun calls the handler with panic recovery. A panic is converted to an
// error so the message stays in the queue for retry instead of crashing the
// worker goroutine.
func safeRun(ctx context.Context, h TaskHandlerFunc, payload json.RawMessage) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("task handler panicked: %v", r)
		}
	}()
	return h(ctx, payload)
}

// visibilityHeartbeat periodically extends the SQS visibility timeout for a
// message while the handler is running. Stops when heartbeatCtx is cancelled.
func visibilityHeartbeat(ctx context.Context, cfg *WorkerConfig, receiptHandle string, taskTimeout time.Duration) {
	// Extend every min(taskTimeout/2, 5 min), with a floor of 30 seconds.
	interval := 5 * time.Minute
	if taskTimeout > 0 && taskTimeout/2 < interval {
		interval = taskTimeout / 2
	}
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}

	// Each extension sets visibility to 2x the interval, so there's always
	// a buffer before the message becomes visible again.
	extension := int32(interval.Seconds() * 2)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := cfg.SQSClient.ChangeVisibility(ctx, receiptHandle, extension); err != nil {
				if ctx.Err() != nil {
					return
				}
				cfg.Logger.Printf("mirrorstack: visibility heartbeat failed: %v", err)
			}
		}
	}
}
