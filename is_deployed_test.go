package mirrorstack_test

import (
	"testing"

	ms "github.com/mirrorstack-ai/app-module-sdk"
	"github.com/mirrorstack-ai/app-module-sdk/internal/runtime"
)

// TestIsDeployed_ForwardsRuntimeDetection pins ms.IsDeployed as the exported
// OR of the two internal/runtime detections, so modules get one owner for
// the deployed-vs-local decision instead of re-reading env vars themselves
// (issue #159).
func TestIsDeployed_ForwardsRuntimeDetection(t *testing.T) {
	want := runtime.IsLambda() || runtime.IsOneShot()
	if got := ms.IsDeployed(); got != want {
		t.Errorf("ms.IsDeployed() = %v, want %v (runtime.IsLambda()=%v, runtime.IsOneShot()=%v)",
			got, want, runtime.IsLambda(), runtime.IsOneShot())
	}
}

// TestIsTaskWorker_ForwardsRuntimeDetection pins ms.IsTaskWorker as an alias
// for runtime.IsOneShot.
func TestIsTaskWorker_ForwardsRuntimeDetection(t *testing.T) {
	want := runtime.IsOneShot()
	if got := ms.IsTaskWorker(); got != want {
		t.Errorf("ms.IsTaskWorker() = %v, want %v", got, want)
	}
}
