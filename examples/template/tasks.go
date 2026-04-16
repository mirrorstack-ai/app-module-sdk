package main

// CLI flag: --use-ecs
// Remove this file if the module has no long-running background work.
//
// Task registration means the module will also be deployed as an ECS task
// worker process (polling SQS). See the SDK README for the deploy-time
// implications of this flag.

import (
	"context"
	"encoding/json"

	ms "github.com/mirrorstack-ai/app-module-sdk"
)

func init() {
	postInitHooks = append(postInitHooks, registerTasks)
}

// TranscodePayload is the shape the caller passes to RunTask.
type TranscodePayload struct {
	VideoID string `json:"videoId"`
	Preset  string `json:"preset"`
}

func registerTasks() {
	ms.OnTask("transcode", func(ctx context.Context, raw json.RawMessage) error {
		var p TranscodePayload
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		// Long-running work; ctx is cancelled on SIGTERM.
		_ = p
		return nil
	})
}
