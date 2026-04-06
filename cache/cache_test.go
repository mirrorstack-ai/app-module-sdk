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
