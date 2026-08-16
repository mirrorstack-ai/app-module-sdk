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
	"mime"
	"strings"
	"sync"
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
	// Get reads an object the MODULE needs, over the S3 API. Distinct from
	// PresignGet, which builds a URL for a VIEWER against the client-facing
	// endpoint — an address the module itself may not be able to reach.
	Get(ctx context.Context, key string) ([]byte, error)
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
	expiresAt time.Time
	now       func() time.Time
	provider  CredentialProvider
	scope     renewableScope
	mu        sync.Mutex
}

// renewableScope retains only immutable resource coordinates. Temporary AWS
// key material must live solely in the AWS credential callback, not in a
// long-lived client field copied by ForApp.
type renewableScope struct {
	bucket  string
	region  string
	prefix  string
	cdnBase string
}

func scopeFromCredential(credential Credential) renewableScope {
	return renewableScope{
		bucket: credential.Bucket, region: credential.Region,
		prefix: credential.Prefix, cdnBase: credential.CDNBase,
	}
}

// NewFromCredential creates a Client from platform-injected STS credentials.
func NewFromCredential(cred Credential) (*Client, error) {
	return newClient(cred, "", "")
}

// NewFromProvider creates a storage client whose AWS credential provider asks
// for current scoped credentials for operations and presigns. Resource scope
// fields are immutable; only key material and expiry may rotate.
func NewFromProvider(ctx context.Context, provider CredentialProvider) (*Client, error) {
	if provider == nil {
		return nil, fmt.Errorf("mirrorstack/storage: renewable credential provider is missing")
	}
	initial, err := provider.Credential(ctx)
	if err != nil {
		return nil, fmt.Errorf("mirrorstack/storage: renewable credential unavailable: %w", err)
	}
	if err := initial.validate(); err != nil {
		return nil, err
	}
	c, err := newClientWithAWSProvider(initial, provider)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// Open is retained for source compatibility with older SDK consumers. An
// unscoped process-wide client can no longer be constructed safely; module
// code must declare ms.RequireStorage and resolve ms.Storage per request so the
// app identity participates in the credential scope.
//
// Deprecated: use ms.RequireStorage during startup and ms.Storage(ctx) in a
// request handler.
func Open(context.Context) (*Client, error) {
	return nil, errors.New("mirrorstack/storage: Open is no longer safe because it cannot derive an app scope — use ms.RequireStorage() during startup and ms.Storage(ctx) per request")
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
		expiresAt: cred.ExpiresAt,
		now:       time.Now,
	}, nil
}

type awsRenewableProvider struct {
	provider CredentialProvider
	scope    renewableScope
	client   *Client
}

func (p *awsRenewableProvider) Retrieve(ctx context.Context) (aws.Credentials, error) {
	current, err := p.provider.Credential(ctx)
	if err != nil {
		return aws.Credentials{}, fmt.Errorf("mirrorstack/storage: renewable credential unavailable: %w", err)
	}
	if err := current.validate(); err != nil {
		return aws.Credentials{}, err
	}
	if current.Bucket != p.scope.bucket || current.Region != p.scope.region || current.Prefix != p.scope.prefix || current.CDNBase != p.scope.cdnBase {
		return aws.Credentials{}, fmt.Errorf("mirrorstack/storage: renewable credential changed storage scope")
	}
	if p.client != nil {
		p.client.mu.Lock()
		p.client.expiresAt = current.ExpiresAt
		p.client.mu.Unlock()
	}
	return aws.Credentials{
		AccessKeyID: current.AccessKeyID, SecretAccessKey: current.SecretAccessKey,
		SessionToken: current.SessionToken, Source: "MirrorStackTaskBroker",
		CanExpire: !current.ExpiresAt.IsZero(), Expires: current.ExpiresAt,
	}, nil
}

func newClientWithAWSProvider(initial Credential, provider CredentialProvider) (*Client, error) {
	scope := scopeFromCredential(initial)
	c := &Client{bucket: initial.Bucket, prefix: initial.Prefix, cdnBase: initial.CDNBase, expiresAt: initial.ExpiresAt, now: time.Now, provider: provider, scope: scope}
	awsProvider := &awsRenewableProvider{provider: provider, scope: scope, client: c}
	cfg := aws.Config{Region: initial.Region, Credentials: awsProvider}
	c.s3Client = s3.NewFromConfig(cfg)
	c.presigner = s3.NewPresignClient(c.s3Client)
	return c, nil
}

// ForApp is retained for source compatibility but can no longer rescope a
// vended credential. Prefix and CDN base are platform-owned because the public
// CDN path must remain inside the same app/module boundary as the IAM policy.
//
// Deprecated: resolve ms.Storage(ctx) separately for each request.
func (c *Client) ForApp(_, _ string) *Client {
	c.mu.Lock()
	expiresAt := c.expiresAt
	c.mu.Unlock()
	return &Client{
		presigner: c.presigner,
		s3Client:  c.s3Client,
		bucket:    c.bucket,
		prefix:    c.prefix,
		cdnBase:   c.cdnBase,
		expiresAt: expiresAt,
		now:       c.now,
		provider:  c.provider,
		scope:     c.scope,
	}
}

// ErrNoCredential is returned when PresignPut/PresignGet is called without storage credentials.
// A deployed invocation must receive credentials regardless of route class;
// public delivery routes may legitimately use storage too.
var ErrNoCredential = fmt.Errorf("mirrorstack/storage: no storage credentials — presigned URLs require authenticated context")

// ErrNotDeclared is returned when module code calls ms.Storage without first
// declaring ms.RequireStorage during startup. Undeclared modules receive no
// storage credential from the platform.
var ErrNotDeclared = errors.New("mirrorstack/storage: storage was not declared — call ms.RequireStorage() during module startup before using ms.Storage(ctx)")

// ErrNotVended is returned when a deployed invocation carries no storage credential.
var ErrNotVended = errors.New("mirrorstack/storage: the platform did not vend storage credentials for this invocation (envelope resources.storage was absent) — this is a PLATFORM misconfiguration, not a module bug: the operator must wire dispatch's module-storage vend (MS_MODULE_STORAGE_ROLE_ARN / _BUCKET / _CDN_BASE)")

// ErrCredentialExpired is returned when a vended STS credential has no safe
// lifetime left for a new presigned URL.
var ErrCredentialExpired = errors.New("mirrorstack/storage: storage credential expired before a safe presigned URL could be created")

// UnderLambda reports whether the process is running under AWS Lambda.
func UnderLambda() bool { return lambdaenv.IsSet() }

const credentialExpirySafetyMargin = 30 * time.Second

func (c *Client) presignTTL(ctx context.Context, requested time.Duration) (time.Duration, error) {
	if requested <= 0 {
		return 0, fmt.Errorf("mirrorstack/storage: presign expiry must be positive")
	}
	if c.provider != nil {
		// Force retrieval before every presign so its TTL is capped by the same
		// current credential that signs the request (AWS may otherwise cache it).
		if _, err := (&awsRenewableProvider{provider: c.provider, scope: c.scope, client: c}).Retrieve(ctx); err != nil {
			return 0, err
		}
	}
	c.mu.Lock()
	expiresAt := c.expiresAt
	c.mu.Unlock()
	if expiresAt.IsZero() {
		return requested, nil
	}
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	remaining := expiresAt.Sub(now()) - credentialExpirySafetyMargin
	if remaining <= 0 {
		return 0, ErrCredentialExpired
	}
	if requested > remaining {
		return remaining, nil
	}
	return requested, nil
}

// PresignPut generates a presigned S3 PUT URL for uploading a file.
// Requires storage credentials in context (Platform/Internal routes only).
func (c *Client) PresignPut(ctx context.Context, key string, expires time.Duration) (string, error) {
	if err := c.requireCredential(); err != nil {
		return "", err
	}
	if err := validateKey(key); err != nil {
		return "", err
	}
	expires, err := c.presignTTL(ctx, expires)
	if err != nil {
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
	return c.presignGet(ctx, key, "", expires)
}

// PresignDownload generates a direct presigned GET whose response is an
// attachment. The signed response override works across origins and keeps
// large objects on the direct S3 path.
func (c *Client) PresignDownload(ctx context.Context, key, filename string, expires time.Duration) (string, error) {
	filename = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, filename))
	if filename == "" {
		filename = "download"
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": filename})
	if disposition == "" {
		return "", fmt.Errorf("mirrorstack/storage: invalid download filename")
	}
	return c.presignGet(ctx, key, disposition, expires)
}

func (c *Client) presignGet(ctx context.Context, key, disposition string, expires time.Duration) (string, error) {
	if err := c.requireCredential(); err != nil {
		return "", err
	}
	if err := validateKey(key); err != nil {
		return "", err
	}
	expires, err := c.presignTTL(ctx, expires)
	if err != nil {
		return "", err
	}
	fullKey := c.prefix + key
	input := &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(fullKey),
	}
	if disposition != "" {
		input.ResponseContentDisposition = aws.String(disposition)
	}
	req, err := c.presigner.PresignGetObject(ctx, input, s3.WithPresignExpires(expires))
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
