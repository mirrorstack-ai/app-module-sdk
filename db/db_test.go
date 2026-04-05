package db

import (
	"context"
	"testing"
)

func TestWithSchema(t *testing.T) {
	ctx := context.Background()

	if s := SchemaFrom(ctx); s != "" {
		t.Errorf("expected empty schema, got %q", s)
	}

	ctx = WithSchema(ctx, "app_abc123")
	if s := SchemaFrom(ctx); s != "app_abc123" {
		t.Errorf("expected 'app_abc123', got %q", s)
	}
}

func TestNew_InvalidURL(t *testing.T) {
	_, err := New(context.Background(), "not-a-url")
	if err == nil {
		t.Error("expected error for invalid connection string")
	}
}
