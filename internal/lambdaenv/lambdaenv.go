// Package lambdaenv reports whether the process is running inside AWS Lambda.
// It is a leaf package (no internal imports) so any SDK package can import it
// without creating a cycle.
package lambdaenv

import "os"

// VarName is the canonical AWS environment variable set by the Lambda runtime.
const VarName = "AWS_LAMBDA_FUNCTION_NAME"

// IsSet reports whether the process is running inside AWS Lambda.
func IsSet() bool {
	return os.Getenv(VarName) != ""
}
