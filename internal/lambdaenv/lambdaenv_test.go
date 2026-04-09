package lambdaenv_test

import (
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/internal/lambdaenv"
)

func TestIsSet(t *testing.T) {
	t.Setenv(lambdaenv.VarName, "")
	if lambdaenv.IsSet() {
		t.Error("IsSet() = true when AWS_LAMBDA_FUNCTION_NAME is unset")
	}

	t.Setenv(lambdaenv.VarName, "test-fn")
	if !lambdaenv.IsSet() {
		t.Error("IsSet() = false when AWS_LAMBDA_FUNCTION_NAME is set")
	}
}
