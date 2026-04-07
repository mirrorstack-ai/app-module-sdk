package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// MultipartUpload manages a multipart S3 upload.
// Browser uploads parts in parallel via presigned URLs, then calls Complete.
//
// The client field is invariant non-nil after construction by CreateMultipart.
// Do not construct MultipartUpload directly in tests — use CreateMultipart so
// the client/bucket/key/uploadID invariants hold.
type MultipartUpload struct {
	client   *Client
	bucket   string
	key      string
	uploadID string
}

// CompletedPart represents a successfully uploaded part.
type CompletedPart struct {
	PartNumber int32
	ETag       string
}

// CreateMultipart starts a multipart upload and returns a MultipartUpload handle.
// Use this for files larger than ~100MB.
//
//	upload, err := s.CreateMultipart(ctx, "video.mp4", "video/mp4")
//	part1URL, _ := upload.PresignPart(ctx, 1, 15*time.Minute)
//	part2URL, _ := upload.PresignPart(ctx, 2, 15*time.Minute)
//	// browser uploads parts in parallel
//	upload.Complete(ctx, []CompletedPart{{1, etag1}, {2, etag2}})
func (c *Client) CreateMultipart(ctx context.Context, key, contentType string) (*MultipartUpload, error) {
	if err := c.requireCredential(); err != nil {
		return nil, err
	}
	if err := validateKey(key); err != nil {
		return nil, err
	}
	fullKey := c.prefix + key
	out, err := c.s3Client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket:      aws.String(c.bucket),
		Key:         aws.String(fullKey),
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return nil, fmt.Errorf("mirrorstack/storage: create multipart failed: %w", err)
	}
	if out.UploadId == nil {
		return nil, fmt.Errorf("mirrorstack/storage: S3 returned nil UploadId")
	}
	return &MultipartUpload{
		client:   c,
		bucket:   c.bucket,
		key:      fullKey,
		uploadID: *out.UploadId,
	}, nil
}

// PresignPart generates a presigned URL for uploading a single part.
// Part numbers must be in [1, 10000] (S3 limit). Browser PUTs the data to this URL.
func (u *MultipartUpload) PresignPart(ctx context.Context, partNumber int32, expires time.Duration) (string, error) {
	if err := u.client.requireCredential(); err != nil {
		return "", err
	}
	if partNumber < 1 || partNumber > 10000 {
		return "", fmt.Errorf("mirrorstack/storage: partNumber must be between 1 and 10000, got %d", partNumber)
	}
	req, err := u.client.presigner.PresignUploadPart(ctx, &s3.UploadPartInput{
		Bucket:     aws.String(u.bucket),
		Key:        aws.String(u.key),
		UploadId:   aws.String(u.uploadID),
		PartNumber: aws.Int32(partNumber),
	}, s3.WithPresignExpires(expires))
	if err != nil {
		return "", fmt.Errorf("mirrorstack/storage: presign part %d failed: %w", partNumber, err)
	}
	return req.URL, nil
}

// Complete finalizes the multipart upload with the list of completed parts.
// Each part must include the PartNumber and the ETag returned by S3 after upload.
func (u *MultipartUpload) Complete(ctx context.Context, parts []CompletedPart) error {
	if err := u.client.requireCredential(); err != nil {
		return err
	}
	if len(parts) == 0 {
		return fmt.Errorf("mirrorstack/storage: Complete called with no parts")
	}
	s3Parts := make([]types.CompletedPart, len(parts))
	for i, p := range parts {
		if p.PartNumber < 1 || p.PartNumber > 10000 {
			return fmt.Errorf("mirrorstack/storage: parts[%d].PartNumber must be between 1 and 10000, got %d", i, p.PartNumber)
		}
		s3Parts[i] = types.CompletedPart{
			PartNumber: aws.Int32(p.PartNumber),
			ETag:       aws.String(p.ETag),
		}
	}
	_, err := u.client.s3Client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   aws.String(u.bucket),
		Key:      aws.String(u.key),
		UploadId: aws.String(u.uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{
			Parts: s3Parts,
		},
	})
	if err != nil {
		return fmt.Errorf("mirrorstack/storage: complete multipart failed: %w", err)
	}
	return nil
}

// Abort cancels the multipart upload. Call this if the upload fails or is abandoned.
// S3 will clean up the parts. Also add a lifecycle rule to auto-abort after 24h.
func (u *MultipartUpload) Abort(ctx context.Context) error {
	if err := u.client.requireCredential(); err != nil {
		return err
	}
	_, err := u.client.s3Client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(u.bucket),
		Key:      aws.String(u.key),
		UploadId: aws.String(u.uploadID),
	})
	if err != nil {
		return fmt.Errorf("mirrorstack/storage: abort multipart failed: %w", err)
	}
	return nil
}

