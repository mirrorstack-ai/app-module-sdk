package taskenv_test

import (
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/internal/taskenv"
)

func TestIsSet(t *testing.T) {
	t.Setenv(taskenv.VarName, "")
	if taskenv.IsSet() {
		t.Error("IsSet() = true when MS_TASK_WORKER_MODE is unset")
	}

	t.Setenv(taskenv.VarName, "false")
	if taskenv.IsSet() {
		t.Error("IsSet() = true when MS_TASK_WORKER_MODE is 'false'")
	}

	t.Setenv(taskenv.VarName, "true")
	if !taskenv.IsSet() {
		t.Error("IsSet() = false when MS_TASK_WORKER_MODE is 'true'")
	}
}
