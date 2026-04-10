package runtime

import "testing"

func TestIsLambda_NotSet(t *testing.T) {
	orig := isLambda
	t.Cleanup(func() { isLambda = orig })

	isLambda = false
	if IsLambda() {
		t.Error("expected false")
	}
}

func TestIsLambda_Set(t *testing.T) {
	orig := isLambda
	t.Cleanup(func() { isLambda = orig })

	isLambda = true
	if !IsLambda() {
		t.Error("expected true")
	}
}

func TestIsTaskWorker_NotSet(t *testing.T) {
	orig := isTaskWorker
	t.Cleanup(func() { isTaskWorker = orig })

	isTaskWorker = false
	if IsTaskWorker() {
		t.Error("expected false")
	}
}

func TestIsTaskWorker_Set(t *testing.T) {
	orig := isTaskWorker
	t.Cleanup(func() { isTaskWorker = orig })

	isTaskWorker = true
	if !IsTaskWorker() {
		t.Error("expected true")
	}
}
