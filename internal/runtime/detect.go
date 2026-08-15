package runtime

import (
	"github.com/mirrorstack-ai/app-module-sdk/internal/lambdaenv"
	"github.com/mirrorstack-ai/app-module-sdk/internal/taskenv"
)

// isLambda is evaluated once at process start. The Lambda environment
// variable is set by the runtime and never changes.
var isLambda = lambdaenv.IsSet()

// isOneShot is evaluated once at process start.
var isTaskWorker = taskenv.IsSet()

// IsLambda reports whether the process is running inside AWS Lambda.
func IsLambda() bool { return isLambda }

// IsOneShot reports whether the process is a managed one-shot task attempt.
func IsOneShot() bool { return isTaskWorker }

// IsTaskWorker is retained internally as a compatibility alias for mode gates.
func IsTaskWorker() bool { return IsOneShot() }
