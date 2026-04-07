package storage

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestForApp_Prefix(t *testing.T) {
	c := &Client{bucket: "test-bucket", prefix: "", cdnBase: "https://cdn.example.com"}
	scoped := c.ForApp("apps/app_abc/mod_media/", "https://media.mirrorstack.ai")

	if scoped.prefix != "apps/app_abc/mod_media/" {
		t.Errorf("expected prefix 'apps/app_abc/mod_media/', got %q", scoped.prefix)
	}
	if scoped.cdnBase != "https://media.mirrorstack.ai" {
		t.Errorf("expected cdnBase 'https://media.mirrorstack.ai', got %q", scoped.cdnBase)
	}
	if scoped.bucket != "test-bucket" {
		t.Errorf("expected bucket 'test-bucket', got %q", scoped.bucket)
	}
}

func TestForApp_SharesS3Client(t *testing.T) {
	c := &Client{s3Client: nil, presigner: nil}
	a := c.ForApp("apps/app_a/mod_x/", "https://cdn.a.com")
	b := c.ForApp("apps/app_b/mod_x/", "https://cdn.b.com")

	if a.s3Client != b.s3Client {
		t.Error("ForApp should share the same S3 client")
	}
}

func TestOpen_ReadsCDNBaseURL(t *testing.T) {
	t.Setenv("CDN_BASE_URL", "https://media.beta.mirrorstack.ai")

	c, err := Open(context.Background())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if c.cdnBase != "https://media.beta.mirrorstack.ai" {
		t.Errorf("cdnBase = %q, want CDN_BASE_URL value", c.cdnBase)
	}
}

func TestURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prefix  string
		cdnBase string
		key     string
		want    string
	}{
		{
			name:    "production",
			prefix:  "apps/app_abc/mod_media/",
			cdnBase: "https://media.mirrorstack.ai",
			key:     "photos/avatar.jpg",
			want:    "https://media.mirrorstack.ai/apps/app_abc/mod_media/photos/avatar.jpg",
		},
		{
			name:    "beta subdomain",
			prefix:  "apps/app_abc/mod_media/",
			cdnBase: "https://media.beta.mirrorstack.ai",
			key:     "photos/avatar.jpg",
			want:    "https://media.beta.mirrorstack.ai/apps/app_abc/mod_media/photos/avatar.jpg",
		},
		{
			name:    "trailing slash",
			prefix:  "apps/app_abc/mod_media/",
			cdnBase: "https://media.mirrorstack.ai/",
			key:     "photos/avatar.jpg",
			want:    "https://media.mirrorstack.ai/apps/app_abc/mod_media/photos/avatar.jpg",
		},
		{
			name:    "dev unscoped",
			prefix:  "",
			cdnBase: "http://localhost:9000/mirrorstack-dev",
			key:     "test.txt",
			want:    "http://localhost:9000/mirrorstack-dev/test.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := &Client{prefix: tt.prefix, cdnBase: tt.cdnBase}
			got, err := c.URL(tt.key)
			if err != nil {
				t.Fatalf("URL: %v", err)
			}
			if got != tt.want {
				t.Errorf("URL = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateKey_Rejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  string
	}{
		{"empty", ""},
		{"leading slash", "/photos/avatar.jpg"},
		{"parent traversal", "../../other_app/secret.jpg"},
		{"middle traversal", "photos/../../../etc/passwd"},
		{"trailing dotdot", "photos/.."},
		{"just dotdot", ".."},
		{"percent-encoded slash dotdot", "photos%2F..%2F..%2Fetc"},
		{"percent-encoded dotdot only", "photos%2Esecret"},
		{"bare percent", "100%discount.pdf"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := validateKey(tt.key); err == nil {
				t.Errorf("validateKey(%q) returned nil, want error", tt.key)
			}
		})
	}
}

func TestURL_RejectsTraversal(t *testing.T) {
	t.Parallel()
	c := &Client{prefix: "apps/app_abc/mod_media/", cdnBase: "https://media.mirrorstack.ai"}
	if _, err := c.URL("../../other_app/secret.jpg"); err == nil {
		t.Error("URL should reject path traversal")
	}
}

func TestURL_RejectsEmptyCDNBase(t *testing.T) {
	t.Parallel()
	c := &Client{prefix: "apps/app_abc/mod_media/", cdnBase: ""}
	if _, err := c.URL("photos/avatar.jpg"); err == nil {
		t.Error("URL should reject empty cdnBase")
	}
}

func TestRequireCredential(t *testing.T) {
	t.Parallel()

	empty := &Client{}
	if err := empty.requireCredential(); err == nil {
		t.Error("expected ErrNoCredential for nil presigner+s3Client")
	}

	// Both fields populated → no error.
	full := &Client{presigner: &s3.PresignClient{}, s3Client: &s3.Client{}}
	if err := full.requireCredential(); err != nil {
		t.Errorf("expected nil for fully constructed client, got %v", err)
	}
}

func TestOpen_LambdaGuard(t *testing.T) {
	t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "app-mod-media")
	_, err := Open(context.Background())
	if err == nil {
		t.Error("expected error in Lambda environment")
	}
}

func TestCredentialContext(t *testing.T) {
	ctx := context.Background()
	if c := CredentialFrom(ctx); c != nil {
		t.Error("expected nil credential")
	}

	cred := Credential{Bucket: "test", Region: "us-east-1", Prefix: "apps/app_a/"}
	ctx = WithCredential(ctx, cred)
	c := CredentialFrom(ctx)
	if c == nil {
		t.Fatal("expected credential")
	}
	if c.Bucket != "test" {
		t.Errorf("expected bucket 'test', got %q", c.Bucket)
	}
}

func TestCredential_Validate(t *testing.T) {
	t.Parallel()

	const secret = "super-secret-access-key"
	full := Credential{
		Bucket:          "mirrorstack-media",
		Region:          "ap-northeast-1",
		Prefix:          "apps/app_abc/mod_media/",
		CDNBase:         "https://media.mirrorstack.ai",
		AccessKeyID:     secret,
		SecretAccessKey: "secret",
	}
	if err := full.validate(); err != nil {
		t.Errorf("full credential should validate, got %v", err)
	}

	cases := []struct {
		name string
		cred Credential
	}{
		{"missing bucket", Credential{Region: "ap-northeast-1", Prefix: "apps/app_a/mod_x/", CDNBase: "https://x", AccessKeyID: secret, SecretAccessKey: "s"}},
		{"missing region", Credential{Bucket: "b", Prefix: "apps/app_a/mod_x/", CDNBase: "https://x", AccessKeyID: secret, SecretAccessKey: "s"}},
		{"missing prefix", Credential{Bucket: "b", Region: "ap-northeast-1", CDNBase: "https://x", AccessKeyID: secret, SecretAccessKey: "s"}},
		{"missing cdnBase", Credential{Bucket: "b", Region: "ap-northeast-1", Prefix: "apps/app_a/mod_x/", AccessKeyID: secret, SecretAccessKey: "s"}},
		{"missing accessKeyID", Credential{Bucket: "b", Region: "ap-northeast-1", Prefix: "apps/app_a/mod_x/", CDNBase: "https://x", SecretAccessKey: "s"}},
		{"missing secretAccessKey", Credential{Bucket: "b", Region: "ap-northeast-1", Prefix: "apps/app_a/mod_x/", CDNBase: "https://x", AccessKeyID: secret}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cred.validate()
			if err == nil {
				t.Fatal("expected validation error")
			}
			if tt.cred.AccessKeyID != "" && strings.Contains(err.Error(), tt.cred.AccessKeyID) {
				t.Errorf("validation error leaked AccessKeyID: %v", err)
			}
		})
	}
}
