package meter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
)

type fakeLambda struct {
	invoked int
	lastIn  *lambda.InvokeInput
	err     error
}

func (f *fakeLambda) Invoke(ctx context.Context, in *lambda.InvokeInput, _ ...func(*lambda.Options)) (*lambda.InvokeOutput, error) {
	f.invoked++
	f.lastIn = in
	if f.err != nil {
		return nil, f.err
	}
	return &lambda.InvokeOutput{StatusCode: 202}, nil
}

func newTestClient(t *testing.T, fake *fakeLambda) *Client {
	t.Helper()
	return &Client{
		lambdaClient: fake,
		functionARN:  "arn:aws:lambda:us-east-1:123456789012:function:meter-test",
	}
}

func TestNewFromARN_ValidARN(t *testing.T) {
	t.Parallel()
	_, err := NewFromARN(context.Background(), "arn:aws:lambda:us-east-1:123456789012:function:meter")
	if err != nil {
		t.Fatalf("NewFromARN with valid ARN: %v", err)
	}
}

func TestNewFromARN_InvalidARN(t *testing.T) {
	t.Parallel()
	cases := []string{
		"",
		"not-an-arn",
		"arn:aws:s3:::bucket/key", // wrong service
		"arn:aws:lambda::123456789012:function:meter",             // missing region
		"arn:aws:lambda:us-east-1::function:meter",                // missing account
		"arn:aws:lambda:us-east-1:123456789012:function:",         // missing name
		"arn:aws:lambda:us-east-1:123456789012:function:bad name", // space in name
	}
	for _, arn := range cases {
		t.Run(arn, func(t *testing.T) {
			_, err := NewFromARN(context.Background(), arn)
			if err == nil {
				t.Errorf("NewFromARN(%q) should reject invalid ARN", arn)
			}
		})
	}
}

func TestRecord_ProdInvokesLambda(t *testing.T) {
	t.Parallel()
	fake := &fakeLambda{}
	c := newTestClient(t, fake)

	ctx := auth.Set(context.Background(), auth.Identity{AppID: "app_abc", AppRole: "admin"})
	m := c.Scope(ctx, "media")
	if err := m.Record("transcode.minutes", 12); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if fake.invoked != 1 {
		t.Errorf("Lambda invoked %d times, want 1", fake.invoked)
	}
	if *fake.lastIn.FunctionName != "arn:aws:lambda:us-east-1:123456789012:function:meter-test" {
		t.Errorf("wrong function name")
	}
	if fake.lastIn.InvocationType != types.InvocationTypeEvent {
		t.Errorf("invocation type = %v, want Event (async)", fake.lastIn.InvocationType)
	}

	var got Event
	if err := json.Unmarshal(fake.lastIn.Payload, &got); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if got.V != 1 {
		t.Errorf("envelope version = %d, want 1", got.V)
	}
	if got.EventID == "" {
		t.Error("EventID should be set")
	}
	if got.Metric != "transcode.minutes" {
		t.Errorf("metric = %q, want transcode.minutes", got.Metric)
	}
	if got.Value != 12 {
		t.Errorf("value = %g, want 12", got.Value)
	}
	if got.AppIDHint != "app_abc" {
		t.Errorf("appIdHint = %q, want app_abc", got.AppIDHint)
	}
	if got.ModuleIDHint != "media" {
		t.Errorf("moduleIdHint = %q, want media", got.ModuleIDHint)
	}
	if got.RecordedAtHint.IsZero() {
		t.Error("recordedAtHint should be set")
	}
}

func TestRecord_PropagatesLambdaError(t *testing.T) {
	t.Parallel()
	fake := &fakeLambda{err: errors.New("throttled")}
	c := newTestClient(t, fake)

	m := c.Scope(context.Background(), "media")
	err := m.Record("transcode.minutes", 1)
	if err == nil || !strings.Contains(err.Error(), "throttled") {
		t.Errorf("expected wrapped throttled error, got %v", err)
	}
}

func TestRecord_DevModeLogsToStderr(t *testing.T) {
	var buf bytes.Buffer
	c := NewDev(log.New(&buf, "", 0))

	ctx := auth.Set(context.Background(), auth.Identity{AppID: "dev_app"})
	m := c.Scope(ctx, "media")
	if err := m.Record("transcode.minutes", 12); err != nil {
		t.Fatalf("Record: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, `appID="dev_app"`) ||
		!strings.Contains(out, `moduleID="media"`) ||
		!strings.Contains(out, `metric="transcode.minutes"`) ||
		!strings.Contains(out, `value=12`) {
		t.Errorf("unexpected log line: %q", out)
	}
}

func TestRecord_DevMode_EmptyAppIDWhenNoAuth(t *testing.T) {
	var buf bytes.Buffer
	c := NewDev(log.New(&buf, "", 0))

	m := c.Scope(context.Background(), "media")
	_ = m.Record("transcode.minutes", 1)

	if !strings.Contains(buf.String(), `appID=""`) {
		t.Errorf("expected appID=\"\" when context has no auth identity, got: %q", buf.String())
	}
}

func TestRecord_InvalidMetricNamePanics(t *testing.T) {
	t.Parallel()
	fake := &fakeLambda{}
	c := newTestClient(t, fake)
	m := c.Scope(context.Background(), "media")

	bad := []string{"", "has/slash", "has space", "has..dots", "null\x00byte"}
	for _, metric := range bad {
		t.Run(metric, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("expected panic on invalid metric name %q", metric)
				}
			}()
			_ = m.Record(metric, 1)
		})
	}
}
