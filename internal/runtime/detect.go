package runtime

import (
	"github.com/mirrorstack-ai/app-module-sdk/internal/lambdaenv"
	"github.com/mirrorstack-ai/app-module-sdk/internal/taskenv"
)

// isLambda is evaluated once at process start. The Lambda environment
// variable is set by the runtime and never changes.
var isLambda = lambdaenv.IsSet()

// isTaskWorker is evaluated once at process start.
var isTaskWorker = taskenv.IsSet()

// IsLambda reports whether the process is running inside AWS Lambda.
func IsLambda() bool { return isLambda }

// IsTaskWorker reports whether the process is running in ECS task worker mode.
func IsTaskWorker() bool { return isTaskWorker }
