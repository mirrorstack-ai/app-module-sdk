package main

// CLI flag: --use-schedule
// Remove this file if the module doesn't need cron jobs.

import (
	"net/http"

	ms "github.com/mirrorstack-ai/app-module-sdk"
)

func init() {
	postInitHooks = append(postInitHooks, registerSchedule)
}

func registerSchedule() {
	// Cron expression in AWS EventBridge syntax. Platform scheduler invokes
	// the handler via Internal-scope POST at each fire.
	ms.Cron("cleanup", "0 3 * * *", func(w http.ResponseWriter, r *http.Request) {
		// Nightly cleanup work goes here.
		w.WriteHeader(http.StatusNoContent)
	})
}
