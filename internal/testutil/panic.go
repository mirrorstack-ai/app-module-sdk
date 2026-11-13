// Package testutil holds shared test helpers for the module's internal test
// packages.
package testutil

import (
	"fmt"
	"testing"
)

// AssertPanics runs fn and fails the test if fn does not panic. msgFmt and
// args are formatted (as with fmt.Sprintf) to produce the error message used
// when no panic occurred.
func AssertPanics(t *testing.T, fn func(), msgFmt string, args ...any) {
	t.Helper()
	defer func() {
		if r := recover(); r == nil {
			t.Error(fmt.Sprintf(msgFmt, args...))
		}
	}()
	fn()
}
