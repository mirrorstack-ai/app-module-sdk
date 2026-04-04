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
