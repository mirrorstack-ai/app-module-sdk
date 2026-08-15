package taskenv_test

import (
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/internal/taskenv"
)

func TestIsSetRequiresCanonicalOneShotValue(t *testing.T) {
	for _, value := range []string{"", "true", "0"} {
		t.Setenv(taskenv.OneShotVar, value)
		if taskenv.IsSet() {
			t.Fatalf("IsSet() = true for %q", value)
		}
	}
	t.Setenv(taskenv.OneShotVar, "1")
	if !taskenv.IsSet() {
		t.Fatal("IsSet() = false for canonical value 1")
	}
}
