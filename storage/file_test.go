package storage_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/mirrorstack-ai/app-module-sdk/storage"
)

// --- Mocks ---

type mockPresigner struct {
	gotPutKey  string
	gotGetKey  string
	gotBucket  string
	gotContent string
}

func (m *mockPresigner) PresignPutObject(_ context.Context, params *s3.PutObjectInput, _ ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	m.gotPutKey = *params.Key
	m.gotBucket = *params.Bucket
	if params.ContentType != nil {
		m.gotContent = *params.ContentType
	}
	return &v4.PresignedHTTPRequest{
		URL:          "https://s3.amazonaws.com/bucket/" + *params.Key,
		SignedHeader: http.Header{"Host": []string{"s3.amazonaws.com"}},
	}, nil
}

func (m *mockPresigner) PresignGetObject(_ context.Context, params *s3.GetObjectInput, _ ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	m.gotGetKey = *params.Key
	m.gotBucket = *params.Bucket
	return &v4.PresignedHTTPRequest{
		URL: "https://s3.amazonaws.com/bucket/" + *params.Key,
	}, nil
}

type mockDeleter struct {
	gotKey     string
	gotBucket  string
	copySrc    string
	copyDst    string
	copyCalled bool
}

func (m *mockDeleter) DeleteObject(_ context.Context, params *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	m.gotKey = *params.Key
	m.gotBucket = *params.Bucket
	return &s3.DeleteObjectOutput{}, nil
}

func (m *mockDeleter) CopyObject(_ context.Context, params *s3.CopyObjectInput, _ ...func(*s3.Options)) (*s3.CopyObjectOutput, error) {
	m.copyCalled = true
	m.copySrc = *params.CopySource
	m.copyDst = *params.Key
	return &s3.CopyObjectOutput{}, nil
}

type mockEdgeNotifier struct {
	syncCalled   bool
	deleteCalled bool
	gotBucket    string
	gotKey       string
}

func (m *mockEdgeNotifier) NotifySync(_ context.Context, bucket, key string) error {
	m.syncCalled = true
	m.gotBucket = bucket
	m.gotKey = key
	return nil
}

func (m *mockEdgeNotifier) NotifyDelete(_ context.Context, bucket, key string) error {
	m.deleteCalled = true
	m.gotBucket = bucket
	m.gotKey = key
	return nil
}

// --- Path validation ---

func TestFileClient_PathValidation(t *testing.T) {
	fc := storage.NewLocalFileClient(t.TempDir(), "http://localhost:9000", "app-1", "video")

	tests := []struct {
		name string
		path string
	}{
		{"empty", ""},
		{"leading slash", "/videos/file.mp4"},
		{"directory traversal", "../secret/file"},
		{"embedded traversal", "videos/../../etc/passwd"},
		{"backslash", "videos\\file.mp4"},
		{"null byte", "videos/\x00file.mp4"},
		{"url encoded dot-dot", "videos/%2e%2e/secret"},
		{"url encoded slash", "videos/%2froot/file"},
		{"url encoded backslash", "videos/%5c../file"},
		{"mixed case encoding", "videos/%2E%2E/secret"},
	}

	for _, tt := range tests {
		_, err := fc.PresignPut(context.Background(), tt.path, time.Minute, "video/mp4")
		if err == nil {
			t.Errorf("%s: expected error for path %q", tt.name, tt.path)
		}
	}
}

func TestFileClient_ValidPaths(t *testing.T) {
	fc := storage.NewLocalFileClient(t.TempDir(), "http://localhost:9000", "app-1", "video")

	validPaths := []string{
		"file.mp4",
		"videos/raw/abc.mp4",
		"thumbnails/thumb.jpg",
		"hls/720p/index.m3u8",
	}
	for _, p := range validPaths {
		_, err := fc.PresignPut(context.Background(), p, time.Minute, "video/mp4")
		if err != nil {
			t.Errorf("unexpected error for valid path %q: %v", p, err)
		}
	}
}

// --- TTL validation ---

func TestFileClient_TTLValidation(t *testing.T) {
	fc := storage.NewLocalFileClient(t.TempDir(), "http://localhost:9000", "app-1", "video")

	_, err := fc.PresignPut(context.Background(), "file.mp4", 0, "video/mp4")
	if err == nil {
		t.Error("expected error for zero TTL")
	}

	_, err = fc.PresignPut(context.Background(), "file.mp4", -time.Minute, "video/mp4")
	if err == nil {
		t.Error("expected error for negative TTL")
	}

	_, err = fc.PresignPut(context.Background(), "file.mp4", 8*24*time.Hour, "video/mp4")
	if err == nil {
		t.Error("expected error for TTL exceeding max")
	}

	_, err = fc.PresignGet(context.Background(), "file.mp4", storage.MaxPresignTTL)
	if err != nil {
		t.Errorf("max TTL should be allowed: %v", err)
	}
}

// --- S3 backend ---

func TestS3Backend_PresignPut(t *testing.T) {
	presigner := &mockPresigner{}
	deleter := &mockDeleter{}
	fc := storage.NewFileClient(storage.FileClientConfig{Presigner: presigner, Deleter: deleter, Bucket: "my-bucket", AppID: "app-123", ModuleID: "video"})

	result, err := fc.PresignPut(context.Background(), "videos/raw/abc.mp4", 15*time.Minute, "video/mp4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if presigner.gotPutKey != "applications/app-123/video/videos/raw/abc.mp4" {
		t.Errorf("key: got %q", presigner.gotPutKey)
	}
	if presigner.gotBucket != "my-bucket" {
		t.Errorf("bucket: got %q", presigner.gotBucket)
	}
	if presigner.gotContent != "video/mp4" {
		t.Errorf("content-type: got %q", presigner.gotContent)
	}
	if result.URL == "" {
		t.Error("URL should not be empty")
	}
	if result.Headers["Host"] != "s3.amazonaws.com" {
		t.Errorf("headers: got %v", result.Headers)
	}
}

func TestS3Backend_PresignGet(t *testing.T) {
	presigner := &mockPresigner{}
	deleter := &mockDeleter{}
	fc := storage.NewFileClient(storage.FileClientConfig{Presigner: presigner, Deleter: deleter, Bucket: "my-bucket", AppID: "app-123", ModuleID: "video"})

	result, err := fc.PresignGet(context.Background(), "thumbnails/thumb.jpg", time.Hour)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if presigner.gotGetKey != "applications/app-123/video/thumbnails/thumb.jpg" {
		t.Errorf("key: got %q", presigner.gotGetKey)
	}
	if result.URL == "" {
		t.Error("URL should not be empty")
	}
}

func TestS3Backend_Delete_AlsoDeletesEdge(t *testing.T) {
	presigner := &mockPresigner{}
	deleter := &mockDeleter{}
	notifier := &mockEdgeNotifier{}
	fc := storage.NewFileClient(storage.FileClientConfig{Presigner: presigner, Deleter: deleter, Notifier: notifier, Bucket: "my-bucket", AppID: "app-123", ModuleID: "video"})

	err := fc.Delete(context.Background(), "videos/raw/old.mp4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if deleter.gotKey != "applications/app-123/video/videos/raw/old.mp4" {
		t.Errorf("S3 key: got %q", deleter.gotKey)
	}
	if !notifier.deleteCalled {
		t.Error("edge delete should have been called")
	}
	if notifier.gotKey != "applications/app-123/video/videos/raw/old.mp4" {
		t.Errorf("edge key: got %q", notifier.gotKey)
	}
}

func TestS3Backend_Delete_NilNotifier(t *testing.T) {
	presigner := &mockPresigner{}
	deleter := &mockDeleter{}
	fc := storage.NewFileClient(storage.FileClientConfig{Presigner: presigner, Deleter: deleter, Bucket: "my-bucket", AppID: "app-123", ModuleID: "video"})

	err := fc.Delete(context.Background(), "videos/raw/old.mp4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestS3Backend_Sync(t *testing.T) {
	presigner := &mockPresigner{}
	deleter := &mockDeleter{}
	notifier := &mockEdgeNotifier{}
	fc := storage.NewFileClient(storage.FileClientConfig{Presigner: presigner, Deleter: deleter, Notifier: notifier, Bucket: "my-bucket", AppID: "app-123", ModuleID: "video"})

	err := fc.Sync(context.Background(), "thumbnails/thumb.jpg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !notifier.syncCalled {
		t.Error("sync should have been called")
	}
	if notifier.gotBucket != "my-bucket" {
		t.Errorf("bucket: got %q", notifier.gotBucket)
	}
	if notifier.gotKey != "applications/app-123/video/thumbnails/thumb.jpg" {
		t.Errorf("key: got %q", notifier.gotKey)
	}
}

func TestS3Backend_Sync_NilNotifier(t *testing.T) {
	presigner := &mockPresigner{}
	deleter := &mockDeleter{}
	fc := storage.NewFileClient(storage.FileClientConfig{Presigner: presigner, Deleter: deleter, Bucket: "my-bucket", AppID: "app-123", ModuleID: "video"})

	err := fc.Sync(context.Background(), "thumbnails/thumb.jpg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- Local backend ---

func TestLocalBackend_PresignPut(t *testing.T) {
	root := t.TempDir()
	fc := storage.NewLocalFileClient(root, "http://localhost:9000", "app-1", "video")

	result, err := fc.PresignPut(context.Background(), "videos/raw/abc.mp4", 15*time.Minute, "video/mp4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "http://localhost:9000/upload/applications/app-1/video/videos/raw/abc.mp4"
	if result.URL != want {
		t.Errorf("URL: got %q, want %q", result.URL, want)
	}

	dir := filepath.Join(root, "applications/app-1/video/videos/raw")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Error("directory was not created")
	}
}

func TestLocalBackend_PresignGet(t *testing.T) {
	fc := storage.NewLocalFileClient(t.TempDir(), "http://localhost:9000", "app-1", "video")

	result, err := fc.PresignGet(context.Background(), "thumbnails/thumb.jpg", time.Hour)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "http://localhost:9000/files/applications/app-1/video/thumbnails/thumb.jpg"
	if result.URL != want {
		t.Errorf("URL: got %q, want %q", result.URL, want)
	}
}

func TestLocalBackend_Delete(t *testing.T) {
	root := t.TempDir()
	fc := storage.NewLocalFileClient(root, "http://localhost:9000", "app-1", "video")

	filePath := filepath.Join(root, "applications/app-1/video/test.txt")
	os.MkdirAll(filepath.Dir(filePath), 0o755)
	os.WriteFile(filePath, []byte("hello"), 0o644)

	err := fc.Delete(context.Background(), "test.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Error("file should have been deleted")
	}
}

func TestLocalBackend_Delete_NotExists(t *testing.T) {
	fc := storage.NewLocalFileClient(t.TempDir(), "http://localhost:9000", "app-1", "video")

	err := fc.Delete(context.Background(), "nonexistent.txt")
	if err != nil {
		t.Fatalf("delete nonexistent file should not error: %v", err)
	}
}

func TestLocalBackend_Sync_NoOp(t *testing.T) {
	fc := storage.NewLocalFileClient(t.TempDir(), "http://localhost:9000", "app-1", "video")

	err := fc.Sync(context.Background(), "thumbnails/thumb.jpg")
	if err != nil {
		t.Fatalf("sync should be no-op in local dev: %v", err)
	}
}

// --- Soft-delete / Restore (S3) ---

func TestS3Backend_SoftDelete(t *testing.T) {
	presigner := &mockPresigner{}
	deleter := &mockDeleter{}
	notifier := &mockEdgeNotifier{}
	fc := storage.NewFileClient(storage.FileClientConfig{Presigner: presigner, Deleter: deleter, Notifier: notifier, Bucket: "my-bucket", AppID: "app-1", ModuleID: "video"})

	err := fc.SoftDelete(context.Background(), "videos/raw/old.mp4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !deleter.copyCalled {
		t.Fatal("copy should have been called")
	}
	if deleter.copySrc != "my-bucket/applications/app-1/video/videos/raw/old.mp4" {
		t.Errorf("copy src: got %q", deleter.copySrc)
	}
	if deleter.copyDst != "applications/app-1/video/.trash/videos/raw/old.mp4" {
		t.Errorf("copy dst: got %q", deleter.copyDst)
	}
	if deleter.gotKey != "applications/app-1/video/videos/raw/old.mp4" {
		t.Errorf("delete key: got %q", deleter.gotKey)
	}
	if !notifier.deleteCalled {
		t.Error("edge delete should have been called")
	}
}

func TestS3Backend_Restore(t *testing.T) {
	presigner := &mockPresigner{}
	deleter := &mockDeleter{}
	fc := storage.NewFileClient(storage.FileClientConfig{Presigner: presigner, Deleter: deleter, Bucket: "my-bucket", AppID: "app-1", ModuleID: "video"})

	err := fc.Restore(context.Background(), "videos/raw/old.mp4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !deleter.copyCalled {
		t.Fatal("copy should have been called")
	}
	if deleter.copySrc != "my-bucket/applications/app-1/video/.trash/videos/raw/old.mp4" {
		t.Errorf("copy src: got %q", deleter.copySrc)
	}
	if deleter.copyDst != "applications/app-1/video/videos/raw/old.mp4" {
		t.Errorf("copy dst: got %q", deleter.copyDst)
	}
	// Original in trash should be deleted after restore.
	if deleter.gotKey != "applications/app-1/video/.trash/videos/raw/old.mp4" {
		t.Errorf("delete key: got %q", deleter.gotKey)
	}
}

// --- Soft-delete / Restore (Local) ---

func TestLocalBackend_SoftDelete(t *testing.T) {
	root := t.TempDir()
	fc := storage.NewLocalFileClient(root, "http://localhost:9000", "app-1", "video")

	// Create a file to soft-delete.
	filePath := filepath.Join(root, "applications/app-1/video/test.txt")
	os.MkdirAll(filepath.Dir(filePath), 0o755)
	os.WriteFile(filePath, []byte("hello"), 0o644)

	err := fc.SoftDelete(context.Background(), "test.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Original should be gone.
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Error("original file should have been deleted")
	}

	// Trash copy should exist.
	trashPath := filepath.Join(root, "applications/app-1/video/.trash/test.txt")
	data, err := os.ReadFile(trashPath)
	if err != nil {
		t.Fatalf("trash file should exist: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("trash content: got %q, want %q", string(data), "hello")
	}
}

func TestLocalBackend_Restore(t *testing.T) {
	root := t.TempDir()
	fc := storage.NewLocalFileClient(root, "http://localhost:9000", "app-1", "video")

	// Create a file in trash.
	trashPath := filepath.Join(root, "applications/app-1/video/.trash/test.txt")
	os.MkdirAll(filepath.Dir(trashPath), 0o755)
	os.WriteFile(trashPath, []byte("restored"), 0o644)

	err := fc.Restore(context.Background(), "test.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Restored file should exist at original path.
	filePath := filepath.Join(root, "applications/app-1/video/test.txt")
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("restored file should exist: %v", err)
	}
	if string(data) != "restored" {
		t.Errorf("content: got %q, want %q", string(data), "restored")
	}

	// Trash copy should be gone.
	if _, err := os.Stat(trashPath); !os.IsNotExist(err) {
		t.Error("trash file should have been deleted after restore")
	}
}
