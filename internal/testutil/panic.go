// Package testutil holds test helpers shared by the SDK's internal packages.
package testutil

import "testing"

// AssertPanics runs fn and fails the test if fn does not panic. msgFmt and args
// form the failure message used when no panic occurred.
//
// It is the one recover-pattern helper usable from any internal package. It
// asserts only that a panic happened, not what the panic value was; a test that
// needs the value should recover() itself. The inverse check — fn must NOT
// panic — is deliberately not provided: those tests want the panic value in the
// failure message and keep their own recover block.
func AssertPanics(t testing.TB, fn func(), msgFmt string, args ...any) {
	t.Helper()
	defer func() {
		if r := recover(); r == nil {
			t.Errorf(msgFmt, args...)
		}
	}()
	fn()
}
