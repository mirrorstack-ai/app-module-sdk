package mirrorstack

import "testing"

// IsDeployed/IsTaskWorker are fixed at process start from the environment the
// test binary was launched in; the unit suite runs on neither Lambda nor a
// task worker, so both are false and consistent with each other.
func TestIsDeployed_FalseUnderGoTest(t *testing.T) {
	if IsDeployed() {
		t.Error("IsDeployed() = true under go test, want false")
	}
	if IsTaskWorker() {
		t.Error("IsTaskWorker() = true under go test, want false")
	}
}
