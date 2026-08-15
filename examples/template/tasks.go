package main

// CLI flag: --use-ecs
// Remove this file if the module has no long-running background work.
//
// Task registration declares platform-managed work. Standard runs on the exact
// installed Lambda version; Heavy/GPU use one-shot managed runners. See the SDK
// README for the deploy-time implications.

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
