package cache

import (
	"context"
	"errors"
	"testing"
)

func TestForApp_Prefix(t *testing.T) {
	c := &Client{prefix: ""}
	scoped := c.ForApp("app_abc123", "mod_media")

	if scoped.prefix != "app_abc123:mod_media:" {
		t.Errorf("expected 'app_abc123:mod_media:', got %q", scoped.prefix)
	}
}

func TestForApp_EmptySchema(t *testing.T) {
	c := &Client{prefix: ""}
	scoped := c.ForApp("", "mod_media")

	if scoped.prefix != ":mod_media:" {
		t.Errorf("expected ':mod_media:', got %q", scoped.prefix)
	}
}

func TestForApp_SharesConnection(t *testing.T) {
	c := &Client{rdb: nil, prefix: ""}
	a := c.ForApp("app_a", "mod_media")
	b := c.ForApp("app_b", "mod_media")

	// Both share the same underlying rdb pointer
	if a.rdb != b.rdb {
		t.Error("ForApp should share the same Redis client")
	}
}

func TestOpen_LambdaGuard(t *testing.T) {
	t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "app-mod-media")
	_, err := Open(context.Background())
	if err == nil {
		t.Error("expected error in Lambda environment")
	}
}

func TestErrCacheMiss(t *testing.T) {
	err := ErrCacheMiss
	if !errors.Is(err, ErrCacheMiss) {
		t.Error("ErrCacheMiss should be identifiable via errors.Is")
	}
}

// ForApp must accept a REAL app id.
//
// 🔴 THE BUG THIS PINS. idPattern excluded hyphens, so ForApp panicked on every
// genuine app id — they are UUIDs. The existing test passed because it used a
// hand-written "app_abc123" that no caller would ever produce. runtime's
// AppSchemaName exists precisely because app ids carry hyphens; nothing did the
// equivalent here, so the first module to call
// ms.Cache().ForApp(ms.AppID(ctx), …) crashed on its first real request.
func TestForApp_AcceptsARealAppID(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ForApp panicked on a real app id: %v", r)
		}
	}()
	c := &Client{prefix: ""}
	scoped := c.ForApp("bb8a3f8b-1234-5678-9abc-def012345678", "mbb8a3f8b123456789abcdef012345678")
	want := "bb8a3f8b-1234-5678-9abc-def012345678:mbb8a3f8b123456789abcdef012345678:"
	if scoped.prefix != want {
		t.Errorf("prefix = %q, want %q", scoped.prefix, want)
	}
}

// The one thing the pattern actually has to stop is a colon: keys are
// {appID}:{moduleID}:{key}, so an id carrying ':' could climb out of its own
// namespace and read another app's data.
func TestForApp_StillRejectsPrefixEscapes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ appID, moduleID string }{
		{"a:b", "mod"},
		{"app", "m:od"},
		{"app", "mod:"},
		{"APP", "mod"}, // uppercase would make two spellings of one app
		{"app id", "mod"},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("ForApp(%q, %q) did not panic — that id can escape its prefix",
						tc.appID, tc.moduleID)
				}
			}()
			c := &Client{prefix: ""}
			_ = c.ForApp(tc.appID, tc.moduleID)
		}()
	}
}
