package runtime

import "github.com/mirrorstack-ai/app-module-sdk/internal/lambdaenv"

// isLambda is evaluated once at process start. The Lambda environment
// variable is set by the runtime and never changes.
var isLambda = lambdaenv.IsSet()

// IsLambda reports whether the process is running inside AWS Lambda.
func IsLambda() bool { return isLambda }
