// Package taskenv reports whether the process is running in task worker mode.
// It is a leaf package (no internal imports) so any SDK package can import it
// without creating a cycle — mirrors the lambdaenv pattern.
package taskenv

import "os"

// VarName is the environment variable that enables task worker mode.
const VarName = "MS_TASK_WORKER_MODE"

// IsSet reports whether the process should run as a task worker.
func IsSet() bool { return os.Getenv(VarName) == "true" }
