package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"golang.org/x/sync/singleflight"
)

var (
	canonicalAppIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	// This mirrors internal/core's Config.ID contract. The local restatement
	// keeps the storage package independent while preventing policy metacharacters.
	devModuleIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,35}$`)
)

const (
	// maxPresignTTL is the longest expiry a module asks for today (video-core: 15 min).
	maxPresignTTL = 900 * time.Second
	// presignSafetyMargin leaves room for request and clock skew around redemption.
	presignSafetyMargin = 300 * time.Second
	// A SigV4 presigned URL carries X-Amz-Security-Token and S3 validates that token on
	// redemption, so the URL's real validity is min(X-Amz-Expires, remaining token lifetime).
	// Re-minting this far ahead of expiry guarantees a presigned URL never outlives its token.
	refreshBuffer = maxPresignTTL + presignSafetyMargin
	// A tunnel can visit many app/module pairs over its lifetime. Keep the
	// parent-key process from retaining every expired scope forever.
	maxDevCredentialCacheEntries = 256
)

// DevConfig is the local S3-compatible (MinIO) plane, read from env.
type DevConfig struct {
	Bucket, Region  string
	Endpoint        string // S3_ENDPOINT — container -> MinIO
	PublicEndpoint  string // S3_PUBLIC_ENDPOINT — browser -> MinIO
	STSEndpoint     string // S3_STS_ENDPOINT — defaults to Endpoint
	CDNBase         string // CDN_BASE_URL
	RoleARN         string // S3_STS_ROLE_ARN
	AccessKeyID     string // AWS_ACCESS_KEY_ID
	SecretAccessKey string // AWS_SECRET_ACCESS_KEY
}

// DevConfigFromEnv reads the explicitly configured local plane. Decision 03
// forbids an AWS fallback: that fallback previously waited on EC2 IMDS and
// returned a 502 while also collapsing tenant scope to the bucket root.
func DevConfigFromEnv() (DevConfig, error) {
	cfg := DevConfig{
		Bucket:          valueOr("S3_BUCKET", "mirrorstack-dev"),
		Region:          valueOr("S3_REGION", "ap-northeast-1"),
		Endpoint:        os.Getenv("S3_ENDPOINT"),
		PublicEndpoint:  os.Getenv("S3_PUBLIC_ENDPOINT"),
		STSEndpoint:     os.Getenv("S3_STS_ENDPOINT"),
		CDNBase:         os.Getenv("CDN_BASE_URL"),
		RoleARN:         valueOr("S3_STS_ROLE_ARN", "arn:minio:iam:::role/module-storage"),
		AccessKeyID:     os.Getenv("AWS_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
	}
	missing := make([]string, 0, 3)
	for name, value := range map[string]string{
		"S3_ENDPOINT": cfg.Endpoint, "AWS_ACCESS_KEY_ID": cfg.AccessKeyID, "AWS_SECRET_ACCESS_KEY": cfg.SecretAccessKey,
	} {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		// Stable ordering makes the diagnostic useful in logs and tests.
		order := []string{"S3_ENDPOINT", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"}
		missing = missing[:0]
		for _, name := range order {
			if os.Getenv(name) == "" {
				missing = append(missing, name)
			}
		}
		return DevConfig{}, fmt.Errorf("mirrorstack/storage: dev storage is not configured (missing %s) — `mirrorstack dev` expects a local S3 (MinIO) at S3_ENDPOINT with S3_PUBLIC_ENDPOINT for browser-reachable presigned URLs; see ms-app-modules/docker-compose.yml. Refusing to fall back to real AWS: an unconfigured fall-back is what made presign hang for 5 s on EC2 IMDS and then 502", strings.Join(missing, ", "))
	}
	if cfg.PublicEndpoint == "" {
		cfg.PublicEndpoint = cfg.Endpoint
	}
	if cfg.STSEndpoint == "" {
		cfg.STSEndpoint = cfg.Endpoint
	}
	if cfg.CDNBase == "" {
		cfg.CDNBase = strings.TrimRight(cfg.PublicEndpoint, "/") + "/" + cfg.Bucket
	}
	return cfg, nil
}

func valueOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

type assumeRoleAPI interface {
	AssumeRole(context.Context, *sts.AssumeRoleInput, ...func(*sts.Options)) (*sts.AssumeRoleOutput, error)
}

type cachedCredential struct {
	credential Credential
	expires    time.Time
}

// DevMinter exchanges the local parent key for prefix-scoped session keys.
type DevMinter struct {
	cfg   DevConfig
	sts   assumeRoleAPI
	now   func() time.Time
	mu    sync.RWMutex
	cache map[string]cachedCredential
	group singleflight.Group
}

// NewDevMinter constructs the local STS client without invoking network I/O.
func NewDevMinter(_ context.Context, cfg DevConfig) (*DevMinter, error) {
	if cfg.Endpoint == "" || cfg.STSEndpoint == "" || cfg.PublicEndpoint == "" || cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, fmt.Errorf("mirrorstack/storage: invalid dev storage configuration: endpoints and parent credentials are required")
	}
	awsCfg := aws.Config{
		Region:      cfg.Region,
		Credentials: credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
	}
	api := sts.NewFromConfig(awsCfg, func(o *sts.Options) { o.BaseEndpoint = aws.String(cfg.STSEndpoint) })
	return &DevMinter{cfg: cfg, sts: api, now: time.Now, cache: make(map[string]cachedCredential)}, nil
}

// Config returns the endpoints needed to construct a dev client after Mint.
func (m *DevMinter) Config() DevConfig { return m.cfg }

// NewFromCredentialForDev constructs the same credential-based client while
// separating container and browser endpoints. Credential validation prevents
// this path (and every other constructor) from ever producing an empty prefix.
func NewFromCredentialForDev(cred Credential, cfg DevConfig) (*Client, error) {
	return newClient(cred, cfg.Endpoint, cfg.PublicEndpoint)
}

// Mint returns a cached credential for the app/module pair or assumes a fresh role.
func (m *DevMinter) Mint(ctx context.Context, appID, moduleID string) (Credential, error) {
	appID, moduleID, err := canonicalizeDevScope(appID, moduleID)
	if err != nil {
		return Credential{}, err
	}
	key := appID + "\x00" + moduleID
	if cred, ok := m.cached(key); ok {
		return cred, nil
	}
	value, err, _ := m.group.Do(key, func() (any, error) {
		if cred, ok := m.cached(key); ok {
			return cred, nil
		}
		return m.mint(ctx, key, appID, moduleID)
	})
	if err != nil {
		return Credential{}, err
	}
	return value.(Credential), nil
}

// canonicalizeDevScope is the sole path for caller-influenced identifiers into
// dev credential cache keys, storage prefixes, and IAM session policies.
func canonicalizeDevScope(appID, moduleID string) (string, string, error) {
	canonicalAppID := strings.ToLower(appID)
	if !canonicalAppIDPattern.MatchString(canonicalAppID) {
		return "", "", fmt.Errorf("mirrorstack/storage: appID must be a canonical UUID — it is interpolated into the IAM session policy Resource, where a `*` would widen the grant to every app")
	}
	if !devModuleIDPattern.MatchString(moduleID) {
		return "", "", fmt.Errorf("mirrorstack/storage: moduleID must match %s — it is interpolated into the IAM session policy Resource, where metacharacters could widen the grant", devModuleIDPattern)
	}
	return canonicalAppID, moduleID, nil
}

func (m *DevMinter) cached(key string) (Credential, bool) {
	m.mu.RLock()
	entry, ok := m.cache[key]
	m.mu.RUnlock()
	return entry.credential, ok && entry.expires.Sub(m.now()) > refreshBuffer
}

func (m *DevMinter) mint(ctx context.Context, cacheKey, appID, moduleID string) (Credential, error) {
	prefix := "apps/" + appID + "/" + moduleID + "/"
	policy, err := devSessionPolicy(m.cfg.Bucket, prefix)
	if err != nil {
		return Credential{}, err
	}
	digest := sha256.Sum256([]byte(appID + "\x00" + moduleID))
	sessionName := "ms-dev-" + hex.EncodeToString(digest[:12])
	duration := int32(3600)
	out, err := m.sts.AssumeRole(ctx, &sts.AssumeRoleInput{
		RoleArn: aws.String(m.cfg.RoleARN), RoleSessionName: aws.String(sessionName),
		Policy: aws.String(policy), DurationSeconds: aws.Int32(duration),
	})
	if err != nil {
		return Credential{}, fmt.Errorf("mirrorstack/storage: dev STS AssumeRole failed: %w", err)
	}
	if out.Credentials == nil || out.Credentials.AccessKeyId == nil || out.Credentials.SecretAccessKey == nil || out.Credentials.Expiration == nil {
		return Credential{}, fmt.Errorf("mirrorstack/storage: dev STS returned incomplete credentials")
	}
	cred := Credential{Bucket: m.cfg.Bucket, Region: m.cfg.Region, Prefix: prefix, CDNBase: m.cfg.CDNBase,
		AccessKeyID: *out.Credentials.AccessKeyId, SecretAccessKey: *out.Credentials.SecretAccessKey,
		ExpiresAt: *out.Credentials.Expiration}
	if out.Credentials.SessionToken != nil {
		cred.SessionToken = *out.Credentials.SessionToken
	}
	if err := cred.validate(); err != nil {
		return Credential{}, err
	}
	m.mu.Lock()
	if _, exists := m.cache[cacheKey]; !exists && len(m.cache) >= maxDevCredentialCacheEntries {
		m.evictOneLocked()
	}
	m.cache[cacheKey] = cachedCredential{credential: cred, expires: *out.Credentials.Expiration}
	m.mu.Unlock()
	return cred, nil
}

func (m *DevMinter) evictOneLocked() {
	var oldestKey string
	var oldestExpiry time.Time
	for key, entry := range m.cache {
		if oldestKey == "" || entry.expires.Before(oldestExpiry) {
			oldestKey = key
			oldestExpiry = entry.expires
		}
	}
	if oldestKey != "" {
		delete(m.cache, oldestKey)
	}
}

type sessionPolicy struct {
	Version   string            `json:"Version"`
	Statement []policyStatement `json:"Statement"`
}
type policyStatement struct {
	Sid      string   `json:"Sid"`
	Effect   string   `json:"Effect"`
	Action   []string `json:"Action"`
	Resource string   `json:"Resource"`
}

func devSessionPolicy(bucket, prefix string) (string, error) {
	p := sessionPolicy{Version: "2012-10-17", Statement: []policyStatement{{
		Sid: "OwnPrefixRW", Effect: "Allow", Action: []string{"s3:GetObject", "s3:PutObject", "s3:AbortMultipartUpload"},
		Resource: "arn:aws:s3:::" + bucket + "/" + prefix + "*",
	}}}
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("mirrorstack/storage: encode dev session policy: %w", err)
	}
	return string(b), nil
}
