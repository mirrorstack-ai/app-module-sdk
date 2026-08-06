package core

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/internal/lambdaenv"
	"github.com/mirrorstack-ai/app-module-sdk/storage"
)

func TestResolveStorageUnderLambdaWithoutCredential(t *testing.T) {
	t.Setenv(lambdaenv.VarName, "test-function")
	m := &Module{config: Config{ID: "media"}}
	_, err := m.resolveStorage(context.Background())
	if !errors.Is(err, storage.ErrNotVended) || !strings.Contains(err.Error(), "PLATFORM") {
		t.Fatalf("err=%v", err)
	}
}

func TestResolveStorageDevRequiresAppScope(t *testing.T) {
	t.Setenv(lambdaenv.VarName, "")
	m := &Module{config: Config{ID: "media"}}
	_, err := m.resolveStorage(context.Background())
	if err == nil || !strings.Contains(err.Error(), "app scope") {
		t.Fatalf("err=%v", err)
	}
}
