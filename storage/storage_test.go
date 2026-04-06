package storage

import (
	"context"
	"testing"
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

func TestURL(t *testing.T) {
	c := &Client{
		bucket:  "test-bucket",
		prefix:  "apps/app_abc/mod_media/",
		cdnBase: "https://media.mirrorstack.ai",
	}

	url := c.URL("photos/avatar.jpg")
	expected := "https://media.mirrorstack.ai/apps/app_abc/mod_media/photos/avatar.jpg"
	if url != expected {
		t.Errorf("expected %q, got %q", expected, url)
	}
}

func TestURL_TrailingSlash(t *testing.T) {
	c := &Client{
		prefix:  "apps/app_abc/mod_media/",
		cdnBase: "https://media.mirrorstack.ai/",
	}

	url := c.URL("photos/avatar.jpg")
	expected := "https://media.mirrorstack.ai/apps/app_abc/mod_media/photos/avatar.jpg"
	if url != expected {
		t.Errorf("expected %q, got %q", expected, url)
	}
}

func TestURL_EmptyPrefix(t *testing.T) {
	c := &Client{prefix: "", cdnBase: "http://localhost:9000/bucket"}

	url := c.URL("test.txt")
	if url != "http://localhost:9000/bucket/test.txt" {
		t.Errorf("unexpected URL: %s", url)
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
