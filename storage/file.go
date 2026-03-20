package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MaxPresignTTL is the maximum allowed TTL for presigned URLs.
const MaxPresignTTL = 7 * 24 * time.Hour // S3 SigV4 maximum

// FileClient provides scoped file operations.
// All paths are relative to the module's prefix:
//
//	<bucket>/applications/<app-id>/<module-id>/<path>
//
// Production: S3 presigned URLs, with optional R2 sync.
// Local dev: filesystem under a configurable root directory.
type FileClient struct {
	backend fileBackend
	prefix  string
}

const trashPrefix = ".trash/"

// fileBackend is the internal interface swapped between prod and dev.
type fileBackend interface {
	presignPut(ctx context.Context, key string, ttl time.Duration, contentType string) (*PresignResult, error)
	presignGet(ctx context.Context, key string, ttl time.Duration) (*PresignResult, error)
	delete(ctx context.Context, key string) error
	copy(ctx context.Context, srcKey, dstKey string) error
	syncToEdge(ctx context.Context, key string) error
	deleteFromEdge(ctx context.Context, key string) error
}

// PresignResult is the response from presign operations.
type PresignResult struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

// EdgeNotifier notifies the platform to sync or delete a file on the edge (R2).
// Decoupled from the event package — modules wire up the implementation.
type EdgeNotifier interface {
	NotifySync(ctx context.Context, bucket, key string) error
	NotifyDelete(ctx context.Context, bucket, key string) error
}

// PresignPut returns a presigned URL for uploading a file.
//
//	result, err := fc.PresignPut(ctx, "videos/raw/abc.mp4", 15*time.Minute, "video/mp4")
func (fc *FileClient) PresignPut(ctx context.Context, path string, ttl time.Duration, contentType string) (*PresignResult, error) {
	if err := validatePath(path); err != nil {
		return nil, err
	}
	if err := validateTTL(ttl); err != nil {
		return nil, err
	}
	result, err := fc.backend.presignPut(ctx, fc.fullKey(path), ttl, contentType)
	if err == nil {
		trackOp(ctx, "file_upload")
	}
	return result, err
}

// PresignGet returns a presigned URL for downloading a file.
//
//	result, err := fc.PresignGet(ctx, "thumbnails/thumb.jpg", 1*time.Hour)
func (fc *FileClient) PresignGet(ctx context.Context, path string, ttl time.Duration) (*PresignResult, error) {
	if err := validatePath(path); err != nil {
		return nil, err
	}
	if err := validateTTL(ttl); err != nil {
		return nil, err
	}
	result, err := fc.backend.presignGet(ctx, fc.fullKey(path), ttl)
	if err == nil {
		trackOp(ctx, "file_download")
	}
	return result, err
}

// Delete removes a file from S3 and notifies the platform to delete from R2.
//
//	err := fc.Delete(ctx, "videos/raw/old.mp4")
func (fc *FileClient) Delete(ctx context.Context, path string) error {
	if err := validatePath(path); err != nil {
		return err
	}
	key := fc.fullKey(path)
	if err := fc.backend.delete(ctx, key); err != nil {
		return err
	}
	trackOp(ctx, "file_delete")
	return fc.backend.deleteFromEdge(ctx, key)
}

// Sync marks a file for S3→R2 edge synchronization.
// The platform handles the actual copy asynchronously.
// Use for files read frequently (thumbnails, HLS segments).
//
//	err := fc.Sync(ctx, "thumbnails/thumb.jpg")
func (fc *FileClient) Sync(ctx context.Context, path string) error {
	if err := validatePath(path); err != nil {
		return err
	}
	return fc.backend.syncToEdge(ctx, fc.fullKey(path))
}

// SoftDelete moves a file to the trash prefix instead of permanently deleting it.
// The file can be restored with Restore. Platform garbage collects trash after a
// configurable retention period.
//
//	err := fc.SoftDelete(ctx, "videos/raw/old.mp4")
//	// File moved to: applications/<app-id>/<module-id>/.trash/videos/raw/old.mp4
func (fc *FileClient) SoftDelete(ctx context.Context, path string) error {
	if err := validatePath(path); err != nil {
		return err
	}
	srcKey := fc.fullKey(path)
	dstKey := fc.fullKey(trashPrefix + path)

	if err := fc.backend.copy(ctx, srcKey, dstKey); err != nil {
		return fmt.Errorf("copy to trash: %w", err)
	}
	if err := fc.backend.delete(ctx, srcKey); err != nil {
		return fmt.Errorf("delete original: %w", err)
	}
	// Also remove from edge cache.
	return fc.backend.deleteFromEdge(ctx, srcKey)
}

// Restore moves a file from the trash back to its original path.
//
//	err := fc.Restore(ctx, "videos/raw/old.mp4")
func (fc *FileClient) Restore(ctx context.Context, path string) error {
	if err := validatePath(path); err != nil {
		return err
	}
	srcKey := fc.fullKey(trashPrefix + path)
	dstKey := fc.fullKey(path)

	if err := fc.backend.copy(ctx, srcKey, dstKey); err != nil {
		return fmt.Errorf("copy from trash: %w", err)
	}
	return fc.backend.delete(ctx, srcKey)
}

// fullKey prepends the scoped prefix to a relative path.
func (fc *FileClient) fullKey(path string) string {
	return fc.prefix + path
}

func validatePath(path string) error {
	if path == "" {
		return errors.New("storage: path must not be empty")
	}
	// Reject null bytes.
	if strings.ContainsRune(path, 0) {
		return errors.New("storage: path must not contain null bytes")
	}
	if strings.HasPrefix(path, "/") {
		return errors.New("storage: path must be relative (no leading /)")
	}
	if strings.Contains(path, "..") {
		return errors.New("storage: path must not contain ..")
	}
	if strings.ContainsRune(path, '\\') {
		return errors.New("storage: path must not contain backslash")
	}
	// Reject URL-encoded traversal sequences (case-insensitive).
	if strings.Contains(path, "%2e") || strings.Contains(path, "%2E") ||
		strings.Contains(path, "%2f") || strings.Contains(path, "%2F") ||
		strings.Contains(path, "%5c") || strings.Contains(path, "%5C") {
		return errors.New("storage: path must not contain percent-encoded traversal sequences")
	}
	return nil
}

func validateTTL(ttl time.Duration) error {
	if ttl <= 0 {
		return errors.New("storage: presign TTL must be positive")
	}
	if ttl > MaxPresignTTL {
		return fmt.Errorf("storage: presign TTL %s exceeds maximum %s", ttl, MaxPresignTTL)
	}
	return nil
}
