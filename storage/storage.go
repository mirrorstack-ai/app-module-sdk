// Package storage provides S3 file storage with per-app prefix isolation and CDN URL generation.
//
// S3 is the source of truth. Cloudflare R2 is the CDN cache layer (Worker handles caching).
// Production: STS credentials injected per invocation via Lambda payload.
// Dev uses locally minted, prefix-scoped STS credentials against MinIO. This
// enforcement is self-imposed because the module process holds MinIO's parent
// key: it gives dev identical key shapes and code paths, cross-app isolation in
// one tunnel runner, and an end-to-end test of production's policy shape. The
// real security wall is the production IAM session policy.
package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/mirrorstack-ai/app-module-sdk/internal/lambdaenv"
)

// Storer is the interface for storage operations.
type Storer interface {
	PresignPut(ctx context.Context, key string, expires time.Duration) (string, error)
	PresignGet(ctx context.Context, key string, expires time.Duration) (string, error)
	URL(key string) (string, error)
	CreateMultipart(ctx context.Context, key, contentType string) (*MultipartUpload, error)
}

// validateKey is hygiene for CDN URL normalization. Browsers normalize paths
// and the AWS SDK percent-decodes while building canonical requests, so keys
// such as "../../other/secret.jpg" are ambiguous at those boundaries. It runs
// inside the module process and is not a security boundary; the IAM session
// policy on the vended credential is the boundary.
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
	prefix    string // "apps/<app>/<module>/"; Credential.validate forbids empty
	cdnBase   string // "https://media.mirrorstack.ai"
}

// NewFromCredential creates a Client from platform-injected STS credentials.
func NewFromCredential(cred Credential) (*Client, error) {
	return newClient(cred, "", "")
}

// newClient separates the endpoint used by server-side S3 calls from the one
// embedded in browser-facing signatures. Empty endpoints preserve AWS endpoint
// resolution, which is the production behavior of NewFromCredential.
func newClient(cred Credential, internalEndpoint, publicEndpoint string) (*Client, error) {
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
	makeS3 := func(endpoint string) *s3.Client {
		return s3.NewFromConfig(cfg, func(o *s3.Options) {
			if endpoint != "" {
				o.BaseEndpoint = aws.String(endpoint)
				o.UsePathStyle = true
			}
		})
	}
	s3Client := makeS3(internalEndpoint)
	presignClient := s3Client
	if publicEndpoint != internalEndpoint {
		presignClient = makeS3(publicEndpoint)
	}
	return &Client{
		presigner: s3.NewPresignClient(presignClient),
		s3Client:  s3Client,
		bucket:    cred.Bucket,
		prefix:    cred.Prefix,
		cdnBase:   cred.CDNBase,
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
// A deployed invocation must receive credentials regardless of route class;
// public delivery routes may legitimately use storage too.
var ErrNoCredential = fmt.Errorf("mirrorstack/storage: no storage credentials — presigned URLs require authenticated context")

// ErrNotVended is returned when a deployed invocation carries no storage credential.
var ErrNotVended = errors.New("mirrorstack/storage: the platform did not vend storage credentials for this invocation (envelope resources.storage was absent) — this is a PLATFORM misconfiguration, not a module bug: the operator must wire dispatch's module-storage vend (MS_MODULE_STORAGE_ROLE_ARN / _BUCKET / _CDN_BASE)")

// UnderLambda reports whether the process is running under AWS Lambda.
func UnderLambda() bool { return lambdaenv.IsSet() }

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
