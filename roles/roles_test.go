package roles

import (
	"slices"
	"testing"
)

func TestAdmin_Key(t *testing.T) {
	t.Parallel()
	if got := Admin().Key; got != "admin" {
		t.Errorf("Admin().Key = %q, want admin", got)
	}
}

func TestViewer_Key(t *testing.T) {
	t.Parallel()
	if got := Viewer().Key; got != "viewer" {
		t.Errorf("Viewer().Key = %q, want viewer", got)
	}
}

func TestCustom_Key(t *testing.T) {
	t.Parallel()
	if got := Custom("moderator").Key; got != "moderator" {
		t.Errorf("Custom(moderator).Key = %q, want moderator", got)
	}
}

func TestCustom_PanicsOnEmpty(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for Custom(\"\")")
		}
	}()
	_ = Custom("")
}

func TestKeys_PreservesOrder(t *testing.T) {
	t.Parallel()
	got := Keys([]Role{Viewer(), Admin(), Custom("moderator")})
	want := []string{"viewer", "admin", "moderator"}
	if !slices.Equal(got, want) {
		t.Errorf("Keys = %v, want %v", got, want)
	}
}

func TestKeys_Empty(t *testing.T) {
	t.Parallel()
	if got := Keys(nil); len(got) != 0 {
		t.Errorf("Keys(nil) = %v, want empty", got)
	}
}
