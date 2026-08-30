package ids_test

import (
	"strings"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/ids"
)

func TestValidUUID(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"123e4567-e89b-12d3-a456-426614174000",
		"123E4567-E89B-12D3-A456-426614174000",
		"00000000-0000-0000-0000-000000000000",
	} {
		if !ids.ValidUUID(value) {
			t.Errorf("ValidUUID(%q) = false, want true", value)
		}
	}

	for _, value := range []string{
		"",
		"123e4567e89b12d3a456426614174000",
		"123e4567-e89b-12d3-a456-42661417400g",
		" 123e4567-e89b-12d3-a456-426614174000",
		"123e4567-e89b-12d3-a456-426614174000 ",
	} {
		if ids.ValidUUID(value) {
			t.Errorf("ValidUUID(%q) = true, want false", value)
		}
	}
}

func TestValidModuleSlug(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"a", "user-core", "module123", "a" + strings.Repeat("1", 15)} {
		if !ids.ValidModuleSlug(value) {
			t.Errorf("ValidModuleSlug(%q) = false, want true", value)
		}
	}
	for _, value := range []string{
		"", "User-core", "1user", "user_core", "user.core", "a" + strings.Repeat("1", 16),
	} {
		if ids.ValidModuleSlug(value) {
			t.Errorf("ValidModuleSlug(%q) = true, want false", value)
		}
	}
}

func TestValidUIComponentName(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"A", "UserProfile", "C" + strings.Repeat("1", 63)} {
		if !ids.ValidUIComponentName(value) {
			t.Errorf("ValidUIComponentName(%q) = false, want true", value)
		}
	}
	for _, value := range []string{
		"", "1Card", "user-card", "user_card", "Card.Export",
		"C" + strings.Repeat("1", 64),
	} {
		if ids.ValidUIComponentName(value) {
			t.Errorf("ValidUIComponentName(%q) = true, want false", value)
		}
	}
}

func TestCanonicalModuleID(t *testing.T) {
	t.Parallel()

	const want = "123e4567-e89b-12d3-a456-426614174000"
	for _, value := range []string{
		want,
		"123E4567-E89B-12D3-A456-426614174000",
		"m123e4567e89b12d3a456426614174000",
		"m123E4567E89B12D3A456426614174000",
	} {
		got, ok := ids.CanonicalModuleID(value)
		if !ok || got != want {
			t.Errorf("CanonicalModuleID(%q) = %q, %v, want %q, true", value, got, ok, want)
		}
	}

	for _, value := range []string{
		"module",
		"M123e4567e89b12d3a456426614174000",
		"m123e4567-e89b-12d3-a456-426614174000",
		"m123e4567e89b12d3a45642661417400g",
		" m123e4567e89b12d3a456426614174000",
		"m123e4567e89b12d3a456426614174000\n",
	} {
		if got, ok := ids.CanonicalModuleID(value); ok || got != "" {
			t.Errorf("CanonicalModuleID(%q) = %q, %v, want empty, false", value, got, ok)
		}
	}
}
