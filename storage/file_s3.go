package storage

import (
	"context"
	"fmt"
	"time"

	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Presigner is the subset of the S3 presign client we need.
type S3Presigner interface {
	PresignPutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
	PresignGetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
}

// S3Deleter is the subset of the S3 client needed for delete and copy.
type S3Deleter interface {
	DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	CopyObject(ctx context.Context, params *s3.CopyObjectInput, optFns ...func(*s3.Options)) (*s3.CopyObjectOutput, error)
}

// s3Backend implements fileBackend using AWS S3.
type s3Backend struct {
	presigner S3Presigner
	deleter   S3Deleter
	notifier  EdgeNotifier
	bucket    string
}

// FileClientConfig holds configuration for creating an S3-backed FileClient.
type FileClientConfig struct {
	Presigner S3Presigner
	Deleter   S3Deleter
	Notifier  EdgeNotifier // nil = no edge sync/delete
	Bucket    string
	AppID     string
	ModuleID  string
}

// NewFileClient creates a FileClient backed by S3 (production).
//
//	presignClient := s3.NewPresignClient(s3Client)
//	fc := storage.NewFileClient(storage.FileClientConfig{
//	    Presigner: presignClient,
//	    Deleter:   s3Client,
//	    Notifier:  edgeNotifier,
//	    Bucket:    "my-bucket",
//	    AppID:     appID,
//	    ModuleID:  moduleID,
//	})
func NewFileClient(cfg FileClientConfig) *FileClient {
	return &FileClient{
		backend: &s3Backend{
			presigner: cfg.Presigner,
			deleter:   cfg.Deleter,
			notifier:  cfg.Notifier,
			bucket:    cfg.Bucket,
		},
		prefix: fmt.Sprintf("applications/%s/%s/", cfg.AppID, cfg.ModuleID),
	}
}

func (b *s3Backend) presignPut(ctx context.Context, key string, ttl time.Duration, contentType string) (*PresignResult, error) {
	input := &s3.PutObjectInput{
		Bucket:      &b.bucket,
		Key:         &key,
		ContentType: &contentType,
	}
	resp, err := b.presigner.PresignPutObject(ctx, input, func(o *s3.PresignOptions) {
		o.Expires = ttl
	})
	if err != nil {
		return nil, err
	}
	headers := make(map[string]string)
	for k, vs := range resp.SignedHeader {
		if len(vs) > 0 {
			headers[k] = vs[0]
		}
	}
	return &PresignResult{URL: resp.URL, Headers: headers}, nil
}

func (b *s3Backend) presignGet(ctx context.Context, key string, ttl time.Duration) (*PresignResult, error) {
	input := &s3.GetObjectInput{
		Bucket: &b.bucket,
		Key:    &key,
	}
	resp, err := b.presigner.PresignGetObject(ctx, input, func(o *s3.PresignOptions) {
		o.Expires = ttl
	})
	if err != nil {
		return nil, err
	}
	return &PresignResult{URL: resp.URL}, nil
}

func (b *s3Backend) copy(ctx context.Context, srcKey, dstKey string) error {
	copySource := b.bucket + "/" + srcKey
	_, err := b.deleter.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     &b.bucket,
		Key:        &dstKey,
		CopySource: &copySource,
	})
	return err
}

func (b *s3Backend) delete(ctx context.Context, key string) error {
	_, err := b.deleter.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &b.bucket,
		Key:    &key,
	})
	return err
}

func (b *s3Backend) syncToEdge(ctx context.Context, key string) error {
	if b.notifier == nil {
		return nil
	}
	return b.notifier.NotifySync(ctx, b.bucket, key)
}

func (b *s3Backend) deleteFromEdge(ctx context.Context, key string) error {
	if b.notifier == nil {
		return nil
	}
	return b.notifier.NotifyDelete(ctx, b.bucket, key)
}
