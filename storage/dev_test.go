package storage

import (
	"context"
	"encoding/json"
	"fmt"
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
	policy, err := devSessionPolicy("media", "apps/app-uuid/module_id/")
	if err != nil {
		t.Fatal(err)
	}
	var got sessionPolicy
	if err := json.Unmarshal([]byte(policy), &got); err != nil {
		t.Fatal(err)
	}
	wantObjectResource := "arn:aws:s3:::media/apps/app-uuid/module_id/*"
	if len(got.Statement) != 2 {
		t.Fatalf("unexpected policy: %s", policy)
	}
	objects, list := got.Statement[0], got.Statement[1]
	if objects.Sid != "OwnPrefixObjects" || objects.Resource != wantObjectResource || strings.Join(objects.Action, ",") != "s3:GetObject,s3:PutObject,s3:DeleteObject,s3:AbortMultipartUpload" {
		t.Fatalf("unexpected object policy: %s", policy)
	}
	if list.Sid != "ListOwnPrefix" || list.Resource != "arn:aws:s3:::media" || strings.Join(list.Action, ",") != "s3:ListBucket" || list.Condition == nil || list.Condition.StringLike["s3:prefix"] != "apps/app-uuid/module_id/*" {
		t.Fatalf("unexpected list policy: %s", policy)
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

func TestDevMintCoalescesConcurrentSameScope(t *testing.T) {
	now := time.Now()
	fake := &fakeSTS{now: func() time.Time { return now }, expires: time.Hour}
	m := testMinter(&now, fake)

	const callers = 32
	start := make(chan struct{})
	credentials := make(chan Credential, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			cred, err := m.Mint(context.Background(), "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "video_core")
			if err != nil {
				errs <- err
				return
			}
			credentials <- cred
		}()
	}
	close(start)
	wg.Wait()
	close(credentials)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	for cred := range credentials {
		if cred.AccessKeyID != "key1" {
			t.Fatalf("AccessKeyID=%q, want coalesced key1", cred.AccessKeyID)
		}
	}
	if fake.calls != 1 {
		t.Fatalf("AssumeRole calls=%d, want 1", fake.calls)
	}
}

func TestDevCredentialCacheIsBounded(t *testing.T) {
	now := time.Now()
	fake := &fakeSTS{now: func() time.Time { return now }, expires: time.Hour}
	m := testMinter(&now, fake)
	for i := 0; i <= maxDevCredentialCacheEntries; i++ {
		appID := fmt.Sprintf("00000000-0000-0000-0000-%012x", i)
		if _, err := m.Mint(context.Background(), appID, "mod"); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if got := len(m.cache); got != maxDevCredentialCacheEntries {
		t.Fatalf("cache entries=%d, want bound %d", got, maxDevCredentialCacheEntries)
	}
	if _, ok := m.cache["00000000-0000-0000-0000-000000000000\x00mod"]; ok {
		t.Fatal("oldest credential was not evicted")
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
	validModuleID := "video_core"
	appIDs := []string{
		"*", "?", "a*", "*/*", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/*",
		"../../other", "apps/x", "%2e%2e", "AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA/*", "",
		strings.Repeat("a", 35), strings.Repeat("a", 37), validAppID + "/", " " + validAppID + " ",
	}
	moduleIDs := []string{"*", "video/core", "video..core", "Video_Core", "video-core", "video%core", ""}

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
	if _, err := testMinter(&now, fake).Mint(context.Background(), "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "video_core"); err != nil {
		t.Fatal(err)
	}
	var policy sessionPolicy
	if err := json.Unmarshal([]byte(aws.ToString(fake.inputs[0].Policy)), &policy); err != nil {
		t.Fatal(err)
	}
	objectResource := policy.Statement[0].Resource
	listPrefix := policy.Statement[1].Condition.StringLike["s3:prefix"]
	if strings.Count(objectResource, "*") != 1 || !strings.HasSuffix(objectResource, "*") {
		t.Fatalf("resource=%q, want exactly one wildcard as the final character", objectResource)
	}
	if strings.Count(listPrefix, "*") != 1 || !strings.HasSuffix(listPrefix, "*") {
		t.Fatalf("list prefix=%q, want exactly one wildcard as the final character", listPrefix)
	}
}

func TestDevMintLowercasesTheAppID(t *testing.T) {
	now := time.Now()
	fake := &fakeSTS{now: func() time.Time { return now }, expires: time.Hour}
	cred, err := testMinter(&now, fake).Mint(context.Background(), "AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA", "video_core")
	if err != nil {
		t.Fatal(err)
	}
	wantScope := "apps/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/video_core/"
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
