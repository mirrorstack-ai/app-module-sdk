package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// localFileBackend implements fileBackend using the local filesystem.
// Presign URLs point to a local dev file server.
type localFileBackend struct {
	rootDir  string
	serveURL string
}

// NewLocalFileClient creates a FileClient backed by the local filesystem.
// rootDir is where files are stored on disk.
// serveURL is the base URL of the local dev file server.
//
//	fc := storage.NewLocalFileClient("/tmp/mirrorstack-files", "http://localhost:9000", appID, moduleID)
func NewLocalFileClient(rootDir, serveURL, appID, moduleID string) *FileClient {
	return &FileClient{
		backend: &localFileBackend{
			rootDir:  rootDir,
			serveURL: serveURL,
		},
		prefix: fmt.Sprintf("applications/%s/%s/", appID, moduleID),
	}
}

// resolveAndVerify joins rootDir + key and verifies the result stays under rootDir.
func (b *localFileBackend) resolveAndVerify(key string) (string, error) {
	full := filepath.Join(b.rootDir, key)
	clean := filepath.Clean(full)
	root := filepath.Clean(b.rootDir)
	if !strings.HasPrefix(clean+string(filepath.Separator), root+string(filepath.Separator)) {
		return "", errors.New("storage: resolved path escapes storage root")
	}
	return clean, nil
}

func (b *localFileBackend) presignPut(_ context.Context, key string, _ time.Duration, _ string) (*PresignResult, error) {
	resolved, err := b.resolveAndVerify(key)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return nil, fmt.Errorf("create directory: %w", err)
	}
	return &PresignResult{
		URL: fmt.Sprintf("%s/upload/%s", b.serveURL, key),
	}, nil
}

func (b *localFileBackend) presignGet(_ context.Context, key string, _ time.Duration) (*PresignResult, error) {
	return &PresignResult{
		URL: fmt.Sprintf("%s/files/%s", b.serveURL, key),
	}, nil
}

func (b *localFileBackend) copy(_ context.Context, srcKey, dstKey string) error {
	srcPath, err := b.resolveAndVerify(srcKey)
	if err != nil {
		return err
	}
	dstPath, err := b.resolveAndVerify(dstKey)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	return os.WriteFile(dstPath, data, 0o644)
}

func (b *localFileBackend) delete(_ context.Context, key string) error {
	resolved, err := b.resolveAndVerify(key)
	if err != nil {
		return err
	}
	err = os.Remove(resolved)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (b *localFileBackend) syncToEdge(_ context.Context, _ string) error {
	return nil
}

func (b *localFileBackend) deleteFromEdge(_ context.Context, _ string) error {
	return nil
}
