package runtime

import "os"

// isLambda is evaluated once at process start. The Lambda environment
// variable is set by the runtime and never changes.
var isLambda = os.Getenv("AWS_LAMBDA_FUNCTION_NAME") != ""

// IsLambda reports whether the process is running inside AWS Lambda.
func IsLambda() bool { return isLambda }
