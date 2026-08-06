package storage

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/aws-sdk-go-v2/service/sts/types"
)

type fakeSTS struct {
	mu      sync.Mutex
	calls   int
	now     func() time.Time
	expires time.Duration
	inputs  []*sts.AssumeRoleInput
}

func (f *fakeSTS) AssumeRole(_ context.Context, in *sts.AssumeRoleInput, _ ...func(*sts.Options)) (*sts.AssumeRoleOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.inputs = append(f.inputs, in)
	n := f.calls
	return &sts.AssumeRoleOutput{Credentials: &types.Credentials{
		AccessKeyId: aws.String("key" + string(rune('0'+n))), SecretAccessKey: aws.String("secret"),
		SessionToken: aws.String("token"), Expiration: aws.Time(f.now().Add(f.expires)),
	}}, nil
}

func testMinter(now *time.Time, fake *fakeSTS) *DevMinter {
	cfg := DevConfig{Bucket: "bucket", Region: "region", Endpoint: "http://s3:9000", PublicEndpoint: "http://localhost:9000", STSEndpoint: "http://s3:9000", CDNBase: "http://localhost:9000/bucket", RoleARN: "arn:minio:iam:::role/module-storage", AccessKeyID: "parent", SecretAccessKey: "secret"}
	return &DevMinter{cfg: cfg, sts: fake, now: func() time.Time { return *now }, cache: make(map[string]cachedCredential)}
}

func TestDevConfigFromEnvFailsLoudly(t *testing.T) {
	for _, missing := range []string{"S3_ENDPOINT", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"} {
		t.Run(missing, func(t *testing.T) {
			t.Setenv("S3_ENDPOINT", "http://s3:9000")
			t.Setenv("AWS_ACCESS_KEY_ID", "parent")
			t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
			t.Setenv(missing, "")
			cfg, err := DevConfigFromEnv()
			if err == nil || !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "Refusing to fall back to real AWS") {
				t.Fatalf("cfg=%+v err=%v", cfg, err)
			}
			if cfg.Endpoint != "" {
				t.Fatalf("failure returned usable endpoint %q", cfg.Endpoint)
			}
		})
	}
}

func TestDevSessionPolicyShape(t *testing.T) {
	policy, err := devSessionPolicy("media", "apps/app-uuid/module-slug/")
	if err != nil {
		t.Fatal(err)
	}
	var got sessionPolicy
	if err := json.Unmarshal([]byte(policy), &got); err != nil {
		t.Fatal(err)
	}
	wantResource := "arn:aws:s3:::media/apps/app-uuid/module-slug/*"
	if len(got.Statement) != 1 || got.Statement[0].Resource != wantResource || strings.Join(got.Statement[0].Action, ",") != "s3:GetObject,s3:PutObject" {
		t.Fatalf("unexpected policy: %s", policy)
	}
}

func TestDevMintRejectsEmptyScope(t *testing.T) {
	now := time.Now()
	fake := &fakeSTS{now: func() time.Time { return now }, expires: time.Hour}
	m := testMinter(&now, fake)
	for _, pair := range [][2]string{{"", "mod"}, {"app", ""}} {
		if _, err := m.Mint(context.Background(), pair[0], pair[1]); err == nil {
			t.Fatal("expected empty scope error")
		}
	}
	if fake.calls != 0 {
		t.Fatalf("STS called %d times", fake.calls)
	}
}

func TestDevMintCachesPerAppModule(t *testing.T) {
	now := time.Now()
	fake := &fakeSTS{now: func() time.Time { return now }, expires: time.Hour}
	m := testMinter(&now, fake)
	for range 2 {
		if _, err := m.Mint(context.Background(), "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "mod"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Mint(context.Background(), "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "mod"); err != nil {
		t.Fatal(err)
	}
	if fake.calls != 2 {
		t.Fatalf("calls=%d, want 2", fake.calls)
	}
	now = now.Add(time.Hour - refreshBuffer)
	if _, err := m.Mint(context.Background(), "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "mod"); err != nil {
		t.Fatal(err)
	}
	if fake.calls != 3 {
		t.Fatalf("calls=%d, want refresh", fake.calls)
	}
}

func TestVendedCredentialOutlivesLongestPresign(t *testing.T) {
	now := time.Now()
	fake := &fakeSTS{now: func() time.Time { return now }, expires: time.Hour}
	m := testMinter(&now, fake)
	if _, err := m.Mint(context.Background(), "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "mod"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour - refreshBuffer - time.Nanosecond)
	if _, ok := m.cached("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa\x00mod"); !ok {
		t.Fatal("cache should still return credential")
	}
	if remaining := time.Hour - (time.Hour - refreshBuffer - time.Nanosecond); remaining <= maxPresignTTL {
		t.Fatalf("remaining=%v", remaining)
	}
}

func TestDevMintRejectsPolicyMetacharacters(t *testing.T) {
	validAppID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	validModuleID := "video-core"
	appIDs := []string{
		"*", "?", "a*", "*/*", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/*",
		"../../other", "apps/x", "%2e%2e", "AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA/*", "",
		strings.Repeat("a", 35), strings.Repeat("a", 37), validAppID + "/", " " + validAppID + " ",
	}
	moduleIDs := []string{"*", "video/core", "video..core", "Video-Core", "video%core", ""}

	for _, appID := range appIDs {
		t.Run("appID="+appID, func(t *testing.T) {
			now := time.Now()
			fake := &fakeSTS{now: func() time.Time { return now }, expires: time.Hour}
			if _, err := testMinter(&now, fake).Mint(context.Background(), appID, validModuleID); err == nil || !strings.Contains(err.Error(), "appID") {
				t.Fatalf("err=%v, want appID validation error", err)
			}
			if fake.calls != 0 {
				t.Fatalf("STS called %d times", fake.calls)
			}
		})
	}
	for _, moduleID := range moduleIDs {
		t.Run("moduleID="+moduleID, func(t *testing.T) {
			now := time.Now()
			fake := &fakeSTS{now: func() time.Time { return now }, expires: time.Hour}
			if _, err := testMinter(&now, fake).Mint(context.Background(), validAppID, moduleID); err == nil || !strings.Contains(err.Error(), "moduleID") {
				t.Fatalf("err=%v, want moduleID validation error", err)
			}
			if fake.calls != 0 {
				t.Fatalf("STS called %d times", fake.calls)
			}
		})
	}
}

func TestDevMintPolicyContainsNoWildcardBeyondTheFinalSegment(t *testing.T) {
	now := time.Now()
	fake := &fakeSTS{now: func() time.Time { return now }, expires: time.Hour}
	if _, err := testMinter(&now, fake).Mint(context.Background(), "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "video-core"); err != nil {
		t.Fatal(err)
	}
	var policy sessionPolicy
	if err := json.Unmarshal([]byte(aws.ToString(fake.inputs[0].Policy)), &policy); err != nil {
		t.Fatal(err)
	}
	resource := policy.Statement[0].Resource
	if strings.Count(resource, "*") != 1 || !strings.HasSuffix(resource, "*") {
		t.Fatalf("resource=%q, want exactly one wildcard as the final character", resource)
	}
}

func TestDevMintLowercasesTheAppID(t *testing.T) {
	now := time.Now()
	fake := &fakeSTS{now: func() time.Time { return now }, expires: time.Hour}
	cred, err := testMinter(&now, fake).Mint(context.Background(), "AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA", "video-core")
	if err != nil {
		t.Fatal(err)
	}
	wantScope := "apps/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/video-core/"
	if cred.Prefix != wantScope {
		t.Fatalf("prefix=%q, want %q", cred.Prefix, wantScope)
	}
	var policy sessionPolicy
	if err := json.Unmarshal([]byte(aws.ToString(fake.inputs[0].Policy)), &policy); err != nil {
		t.Fatal(err)
	}
	if resource := policy.Statement[0].Resource; !strings.Contains(resource, "/"+wantScope+"*") {
		t.Fatalf("resource=%q does not contain canonical scope %q", resource, wantScope)
	}
}

func TestCredentialValidateRequiresTrailingSlash(t *testing.T) {
	cred := Credential{
		Bucket: "b", Region: "region", Prefix: "apps/app/mod", CDNBase: "https://x",
		AccessKeyID: "key", SecretAccessKey: "secret",
	}
	err := cred.validate()
	if err == nil || !strings.Contains(err.Error(), "trailing slash") {
		t.Fatalf("err=%v, want trailing slash validation error", err)
	}
}

func TestPresignUsesPublicEndpoint(t *testing.T) {
	cred := Credential{Bucket: "bucket", Region: "region", Prefix: "apps/app/mod/", CDNBase: "http://localhost:9000/bucket", AccessKeyID: "key", SecretAccessKey: "secret", SessionToken: "token"}
	c, err := newClient(cred, "http://s3:9000", "http://localhost:9000")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := c.PresignPut(context.Background(), "video.mp4", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host != "localhost:9000" {
		t.Fatalf("url=%q host=%q err=%v", raw, u.Host, err)
	}
}
