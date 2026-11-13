package storage

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestCredentialExpiryRoundTripsOnTheWire(t *testing.T) {
	want := time.Date(2026, time.August, 6, 12, 30, 0, 0, time.UTC)
	b, err := json.Marshal(Credential{ExpiresAt: want})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"expiresAt":"2026-08-06T12:30:00Z"`) {
		t.Fatalf("wire credential missing expiry: %s", b)
	}
	var got Credential
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !got.ExpiresAt.Equal(want) {
		t.Fatalf("ExpiresAt=%v, want %v", got.ExpiresAt, want)
	}
}

func TestOpenFailsClosedButRemainsSourceCompatible(t *testing.T) {
	client, err := Open(context.Background())
	if client != nil || err == nil || !strings.Contains(err.Error(), "cannot derive an app scope") {
		t.Fatalf("Open client=%v err=%v, want fail-closed migration error", client, err)
	}
}

func TestPresignTTLStaysWithinCredentialLifetime(t *testing.T) {
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	c := &Client{expiresAt: now.Add(10 * time.Minute), now: func() time.Time { return now }}

	got, err := c.presignTTL(context.Background(), 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if want := 10*time.Minute - credentialExpirySafetyMargin; got != want {
		t.Fatalf("clamped TTL=%v, want %v", got, want)
	}
	if got, err := c.presignTTL(context.Background(), time.Minute); err != nil || got != time.Minute {
		t.Fatalf("short TTL=%v err=%v, want 1m", got, err)
	}
	c.expiresAt = now.Add(credentialExpirySafetyMargin)
	if _, err := c.presignTTL(context.Background(), time.Minute); !errors.Is(err, ErrCredentialExpired) {
		t.Fatalf("expired credential err=%v, want ErrCredentialExpired", err)
	}
}

func TestMultipartPresignStaysWithinCredentialLifetime(t *testing.T) {
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	c, err := newClient(Credential{
		Bucket: "bucket", Region: "ap-northeast-1", Prefix: "apps/app/mod/", CDNBase: "https://cdn.example.com",
		AccessKeyID: "key", SecretAccessKey: "secret", SessionToken: "token", ExpiresAt: now.Add(10 * time.Minute),
	}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return now }
	upload := &MultipartUpload{client: c, bucket: "bucket", key: "apps/app/mod/video.mp4", uploadID: "upload"}
	raw, err := upload.PresignPart(context.Background(), 1, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := parsed.Query().Get("X-Amz-Expires"), "570"; got != want {
		t.Fatalf("X-Amz-Expires=%q, want %q", got, want)
	}
}

func TestPresignDownloadForcesAttachmentWithUnicodeFilename(t *testing.T) {
	c, err := newClient(Credential{
		Bucket: "bucket", Region: "ap-northeast-1", Prefix: "apps/app/mod/", CDNBase: "https://cdn.example.com",
		AccessKeyID: "key", SecretAccessKey: "secret",
	}, "http://localhost:9000", "http://localhost:9000")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := c.PresignDownload(context.Background(), "videos/id/source.mp4", "課程\r\n.mp4", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	disposition := parsed.Query().Get("response-content-disposition")
	mediaType, params, err := mime.ParseMediaType(disposition)
	if err != nil {
		t.Fatalf("parse disposition %q: %v", disposition, err)
	}
	if mediaType != "attachment" || params["filename"] != "課程.mp4" {
		t.Fatalf("disposition=%q filename=%q, want attachment/課程.mp4", mediaType, params["filename"])
	}
	if parsed.Query().Get("X-Amz-Signature") == "" {
		t.Fatal("download response override was not signed")
	}
}

func TestForAppCannotChangeVendedScope(t *testing.T) {
	c := &Client{bucket: "test-bucket", prefix: "apps/app_a/mod_media/", cdnBase: "https://cdn.example.com"}
	scoped := c.ForApp("apps/app_b/mod_media/", "https://attacker.example.com")

	if scoped.prefix != c.prefix {
		t.Errorf("ForApp changed vended prefix to %q", scoped.prefix)
	}
	if scoped.cdnBase != c.cdnBase {
		t.Errorf("ForApp changed platform CDN base to %q", scoped.cdnBase)
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
			name:    "dev scoped",
			prefix:  "apps/app_abc/mod_media/",
			cdnBase: "http://localhost:9000/mirrorstack-dev",
			key:     "test.txt",
			want:    "http://localhost:9000/mirrorstack-dev/apps/app_abc/mod_media/test.txt",
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

func TestMaxUploadPanicsOnNonPositive(t *testing.T) {
	t.Parallel()
	for _, n := range []int64{0, -1} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("MaxUpload(%d) did not panic", n)
				}
			}()
			MaxUpload(n)
		}()
	}
}

func TestMaxUploadSetsRequireSettings(t *testing.T) {
	t.Parallel()
	var s RequireSettings
	MaxUpload(2 << 20)(&s)
	if s.MaxUploadBytes != 2<<20 {
		t.Fatalf("RequireSettings.MaxUploadBytes = %d, want %d", s.MaxUploadBytes, 2<<20)
	}
}

// 🔴 THE CONTENT TYPE MUST BE IN THE SIGNATURE, NOT JUST IN THE REQUEST.
//
// A presigned PUT enforces exactly the headers it signed. While the type was
// absent from PutObjectInput, the client chose it: a module allow-list that
// "only accepts png, jpeg, webp" decided the extension of the KEY and nothing
// about the bytes, so an uploader could store image/svg+xml on a .png key and
// have the CDN replay it cross-origin (core-v2#1469).
//
// The assertion is on X-Amz-SignedHeaders because that is what S3 will check.
// A test that only inspected PutObjectInput would pass against a client that
// built the field and never signed it.
func TestPresignPutSignsTheContentType(t *testing.T) {
	cred := Credential{
		Bucket: "bucket", Region: "region", Prefix: "apps/app/mod/",
		CDNBase: "https://cdn.example", AccessKeyID: "key", SecretAccessKey: "secret",
	}
	c, err := newClient(cred, "", "")
	if err != nil {
		t.Fatal(err)
	}

	raw, err := c.PresignPut(context.Background(), "icon.png", "image/png", time.Minute)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	signed := u.Query().Get("X-Amz-SignedHeaders")
	if !strings.Contains(signed, "content-type") {
		t.Fatalf("X-Amz-SignedHeaders = %q, want it to include content-type — without it S3 accepts "+
			"a PUT declaring any type at all, and the caller's allow-list decides nothing", signed)
	}
}

// A caller that does not know what it is accepting cannot make this decision
// safely, and a default — octet-stream, or "whatever the client says" — is the
// state the parameter exists to end. So an empty type is refused.
func TestPresignPutRefusesAnEmptyContentType(t *testing.T) {
	cred := Credential{
		Bucket: "bucket", Region: "region", Prefix: "apps/app/mod/",
		CDNBase: "https://cdn.example", AccessKeyID: "key", SecretAccessKey: "secret",
	}
	c, err := newClient(cred, "", "")
	if err != nil {
		t.Fatal(err)
	}

	for _, contentType := range []string{"", "   "} {
		if _, err := c.PresignPut(context.Background(), "icon.png", contentType, time.Minute); !errors.Is(err, ErrContentTypeRequired) {
			t.Errorf("PresignPut(%q) error = %v, want ErrContentTypeRequired", contentType, err)
		}
	}
}

// 🔴 WithMaxBytes MUST LAND Content-Length IN THE SIGNATURE, THE SAME WAY
// contentType DOES, OR IT ENFORCES NOTHING.
//
// The v4 signer does not read Content-Length from a header at all — it reads
// http.Request.ContentLength directly (aws-sdk-go-v2's
// httpSigner.buildCanonicalHeaders takes it as a separate parameter and skips
// any same-named header in the header map). So PresignPut's Content-Length
// middleware sets that field rather than calling Header.Set, unlike
// signContentType. This test is the proxy for the "sign for N bytes, PUT a
// different declared length against a dev bucket" experiment the issue calls
// for — this SDK's test suite has no dev R2 bucket to run that against, so it
// asserts the one thing fully within its control: that the byte count reaches
// X-Amz-SignedHeaders at all.
func TestPresignPutSignsContentLengthWhenMaxBytesGiven(t *testing.T) {
	cred := Credential{
		Bucket: "bucket", Region: "region", Prefix: "apps/app/mod/",
		CDNBase: "https://cdn.example", AccessKeyID: "key", SecretAccessKey: "secret",
	}
	c, err := newClient(cred, "", "")
	if err != nil {
		t.Fatal(err)
	}

	raw, err := c.PresignPut(context.Background(), "icon.png", "image/png", time.Minute, WithMaxBytes(1234))
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	signed := u.Query().Get("X-Amz-SignedHeaders")
	if !strings.Contains(signed, "content-length") {
		t.Fatalf("X-Amz-SignedHeaders = %q, want it to include content-length — without it a presigned URL "+
			"accepts a PUT declaring any size at all", signed)
	}
}

// Without WithMaxBytes, nothing about size is signed — this is the baseline
// that the test above is a delta against.
func TestPresignPutDoesNotSignContentLengthByDefault(t *testing.T) {
	cred := Credential{
		Bucket: "bucket", Region: "region", Prefix: "apps/app/mod/",
		CDNBase: "https://cdn.example", AccessKeyID: "key", SecretAccessKey: "secret",
	}
	c, err := newClient(cred, "", "")
	if err != nil {
		t.Fatal(err)
	}

	raw, err := c.PresignPut(context.Background(), "icon.png", "image/png", time.Minute)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if signed := u.Query().Get("X-Amz-SignedHeaders"); strings.Contains(signed, "content-length") {
		t.Fatalf("X-Amz-SignedHeaders = %q, did not expect content-length without WithMaxBytes", signed)
	}
}

func TestWithMaxBytesRefusesNonPositive(t *testing.T) {
	cred := Credential{
		Bucket: "bucket", Region: "region", Prefix: "apps/app/mod/",
		CDNBase: "https://cdn.example", AccessKeyID: "key", SecretAccessKey: "secret",
	}
	c, err := newClient(cred, "", "")
	if err != nil {
		t.Fatal(err)
	}

	for _, n := range []int64{0, -1} {
		if _, err := c.PresignPut(context.Background(), "icon.png", "image/png", time.Minute, WithMaxBytes(n)); !errors.Is(err, ErrInvalidMaxBytes) {
			t.Errorf("PresignPut with WithMaxBytes(%d) error = %v, want ErrInvalidMaxBytes", n, err)
		}
	}
}

// The vended per-module ceiling is a real bound: a module cannot mint a URL
// for more bytes than the platform told the SDK it may ever accept.
func TestPresignPutRefusesDeclaredSizeAboveVendedCeiling(t *testing.T) {
	cred := Credential{
		Bucket: "bucket", Region: "region", Prefix: "apps/app/mod/",
		CDNBase: "https://cdn.example", AccessKeyID: "key", SecretAccessKey: "secret",
		MaxUploadBytes: 1000,
	}
	c, err := newClient(cred, "", "")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.PresignPut(context.Background(), "icon.png", "image/png", time.Minute, WithMaxBytes(1001)); !errors.Is(err, ErrUploadTooLarge) {
		t.Fatalf("PresignPut above ceiling error = %v, want ErrUploadTooLarge", err)
	}
	if _, err := c.PresignPut(context.Background(), "icon.png", "image/png", time.Minute, WithMaxBytes(1000)); err != nil {
		t.Fatalf("PresignPut at ceiling should succeed, got %v", err)
	}
	if got := c.MaxUploadBytes(); got != 1000 {
		t.Fatalf("Client.MaxUploadBytes() = %d, want 1000", got)
	}
}

// An older platform envelope that predates MaxUploadBytes leaves it at 0.
// WithMaxBytes must still sign the declared size — the caller's size control
// does not depend on the platform having adopted the ceiling yet — it just
// cannot be checked against a limit the SDK was never given.
func TestPresignPutSignsSizeEvenWithoutAVendedCeiling(t *testing.T) {
	cred := Credential{
		Bucket: "bucket", Region: "region", Prefix: "apps/app/mod/",
		CDNBase: "https://cdn.example", AccessKeyID: "key", SecretAccessKey: "secret",
	}
	c, err := newClient(cred, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.MaxUploadBytes(); got != 0 {
		t.Fatalf("Client.MaxUploadBytes() = %d, want 0 (no vended ceiling)", got)
	}

	raw, err := c.PresignPut(context.Background(), "icon.png", "image/png", time.Minute, WithMaxBytes(1<<40))
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if signed := u.Query().Get("X-Amz-SignedHeaders"); !strings.Contains(signed, "content-length") {
		t.Fatalf("X-Amz-SignedHeaders = %q, want content-length signed regardless of a known ceiling", signed)
	}
}

// CreateMultipart cannot sign a total across independently-signed parts (see
// its doc comment), but it can refuse to even START an upload whose declared
// total already exceeds the vended ceiling — the same stated-intent check
// PresignPut makes. The check must happen before any S3 call, which is what
// lets this test run without a real S3 client.
func TestCreateMultipartRefusesDeclaredTotalAboveVendedCeiling(t *testing.T) {
	c := &Client{presigner: &s3.PresignClient{}, s3Client: &s3.Client{}, prefix: "apps/app/mod/", maxUploadBytes: 1000}

	if _, err := c.CreateMultipart(context.Background(), "video.mp4", "video/mp4", WithMaxBytes(1001)); !errors.Is(err, ErrUploadTooLarge) {
		t.Fatalf("CreateMultipart above ceiling error = %v, want ErrUploadTooLarge", err)
	}
}
