// Package storage provides S3 file storage with per-app prefix isolation and CDN URL generation.
//
// S3 is the source of truth. Cloudflare R2 is the CDN cache layer (Worker handles caching).
// Production: STS credentials injected per invocation via Lambda payload.
// Dev: S3_BUCKET, S3_REGION, S3_ENDPOINT env vars with local defaults.
package storage

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Storer is the interface for storage operations.
type Storer interface {
	PresignPut(ctx context.Context, key string, expires time.Duration) (string, error)
	PresignGet(ctx context.Context, key string, expires time.Duration) (string, error)
	URL(key string) (string, error)
	CreateMultipart(ctx context.Context, key, contentType string) (*MultipartUpload, error)
}

// validateKey rejects keys that would escape the prefix scope. S3 treats keys
// as opaque (no traversal), but CDN URLs are normalized by browsers and AWS
// SDK percent-decodes during canonical request building — so a key like
// "../../other_app/secret.jpg" or "%2F..%2F..%2Fetc" would resolve to a
// different tenant's path. This is the only path where prefix isolation can
// be bypassed by caller-controlled input.
//
// The "%" reject is conservative: S3 keys do not require percent-encoding
// from the application layer (the SDK handles encoding). Filenames with "%"
// or ".." should be normalized at ingest before being used as storage keys.
func validateKey(key string) error {
	if key == "" {
		return fmt.Errorf("mirrorstack/storage: key must not be empty")
	}
	if strings.HasPrefix(key, "/") {
		return fmt.Errorf("mirrorstack/storage: key must not start with '/'")
	}
	if strings.Contains(key, "..") {
		return fmt.Errorf("mirrorstack/storage: key must not contain '..'")
	}
	if strings.Contains(key, "%") {
		return fmt.Errorf("mirrorstack/storage: key must not contain '%%' (percent-encoded traversal)")
	}
	return nil
}

// requireCredential returns ErrNoCredential if the client was constructed
// without S3 credentials (e.g., on a Public route in production). Both fields
// are checked together so a future constructor that decouples them cannot
// reach a panic at the call site.
func (c *Client) requireCredential() error {
	if c.presigner == nil || c.s3Client == nil {
		return ErrNoCredential
	}
	return nil
}

// Client wraps an S3 presigner with app-scoped key prefix and CDN base URL.
type Client struct {
	presigner *s3.PresignClient
	s3Client  *s3.Client
	bucket    string
	prefix    string // "apps/app_abc/mod_media/"
	cdnBase   string // "https://media.mirrorstack.ai"
}

// NewFromCredential creates a Client from platform-injected STS credentials.
func NewFromCredential(cred Credential) (*Client, error) {
	if err := cred.validate(); err != nil {
		return nil, err
	}
	cfg := aws.Config{
		Region: cred.Region,
		Credentials: credentials.NewStaticCredentialsProvider(
			cred.AccessKeyID,
			cred.SecretAccessKey,
			cred.SessionToken,
		),
	}
	s3Client := s3.NewFromConfig(cfg)
	return &Client{
		presigner: s3.NewPresignClient(s3Client),
		s3Client:  s3Client,
		bucket:    cred.Bucket,
		prefix:    cred.Prefix,
		cdnBase:   cred.CDNBase,
	}, nil
}

// Open creates a Client from env vars for dev mode.
// Cannot be used in Lambda — credentials are injected per invocation.
func Open(ctx context.Context) (*Client, error) {
	if os.Getenv("AWS_LAMBDA_FUNCTION_NAME") != "" {
		return nil, fmt.Errorf("mirrorstack/storage: Open() cannot be used in Lambda — credentials are injected per-invocation")
	}

	bucket := os.Getenv("S3_BUCKET")
	if bucket == "" {
		bucket = "mirrorstack-dev"
	}
	region := os.Getenv("S3_REGION")
	if region == "" {
		region = "ap-northeast-1"
	}
	cdnBase := os.Getenv("CDN_BASE_URL")
	if cdnBase == "" {
		cdnBase = "http://localhost:9000/" + bucket
	}
	endpoint := os.Getenv("S3_ENDPOINT")

	var opts []func(*config.LoadOptions) error
	opts = append(opts, config.WithRegion(region))
	if endpoint != "" {
		// MinIO or local S3-compatible
		opts = append(opts, config.WithBaseEndpoint(endpoint))
	}

	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("mirrorstack/storage: failed to load AWS config: %w", err)
	}

	s3Client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if endpoint != "" {
			o.UsePathStyle = true // MinIO requires path-style
		}
	})

	return &Client{
		presigner: s3.NewPresignClient(s3Client),
		s3Client:  s3Client,
		bucket:    bucket,
		prefix:    "",
		cdnBase:   cdnBase,
	}, nil
}

// ForApp returns a new Client with an app-scoped prefix and CDN base.
// Shares the underlying S3 client.
func (c *Client) ForApp(prefix, cdnBase string) *Client {
	return &Client{
		presigner: c.presigner,
		s3Client:  c.s3Client,
		bucket:    c.bucket,
		prefix:    prefix,
		cdnBase:   cdnBase,
	}
}

// ErrNoCredential is returned when PresignPut/PresignGet is called without storage credentials.
// This happens on Public routes where STS credentials are not injected.
var ErrNoCredential = fmt.Errorf("mirrorstack/storage: no storage credentials — presigned URLs require authenticated context")

// PresignPut generates a presigned S3 PUT URL for uploading a file.
// Requires storage credentials in context (Platform/Internal routes only).
func (c *Client) PresignPut(ctx context.Context, key string, expires time.Duration) (string, error) {
	if err := c.requireCredential(); err != nil {
		return "", err
	}
	if err := validateKey(key); err != nil {
		return "", err
	}
	fullKey := c.prefix + key
	req, err := c.presigner.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(fullKey),
	}, s3.WithPresignExpires(expires))
	if err != nil {
		return "", fmt.Errorf("mirrorstack/storage: presign put failed: %w", err)
	}
	return req.URL, nil
}

// PresignGet generates a presigned S3 GET URL for direct download (bypasses CDN).
// Requires storage credentials in context (Platform/Internal routes only).
func (c *Client) PresignGet(ctx context.Context, key string, expires time.Duration) (string, error) {
	if err := c.requireCredential(); err != nil {
		return "", err
	}
	if err := validateKey(key); err != nil {
		return "", err
	}
	fullKey := c.prefix + key
	req, err := c.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(fullKey),
	}, s3.WithPresignExpires(expires))
	if err != nil {
		return "", fmt.Errorf("mirrorstack/storage: presign get failed: %w", err)
	}
	return req.URL, nil
}

// URL returns the CDN URL for a file. The Cloudflare Worker handles R2 caching:
// R2 hit → serve, R2 miss → fetch from S3 → cache in R2 → serve.
// Returns an error if the key would escape the prefix scope when normalized by a browser.
func (c *Client) URL(key string) (string, error) {
	if err := validateKey(key); err != nil {
		return "", err
	}
	if c.cdnBase == "" {
		return "", fmt.Errorf("mirrorstack/storage: cdnBase not configured")
	}
	base := strings.TrimRight(c.cdnBase, "/")
	return base + "/" + c.prefix + key, nil
}
