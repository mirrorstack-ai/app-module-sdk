// Package meter emits usage events for billing to a platform-owned AWS
// Lambda function via async invoke. See the Meter interface for the API.
//
// Security model: IAM invoke permission scoped to the exact meter ARN is
// the sole access control. Wire-format fields suffixed with Hint (AppIDHint,
// ModuleIDHint, RecordedAtHint) are NOT trusted — the platform meter Lambda
// re-derives authoritative values from the invoker's AWS identity.
package meter

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/internal/ids"
	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
)

// arnPattern matches a valid Lambda function ARN. Validated at Client
// construction so a typo in MS_METER_LAMBDA_ARN fails fast at startup
// rather than at first Record call (silent revenue loss otherwise).
var arnPattern = regexp.MustCompile(`^arn:aws:lambda:[a-z0-9-]+:[0-9]+:function:[a-zA-Z0-9_-]+$`)

// Meter records usage events for billing.
type Meter interface {
	// Record emits a usage event via async Lambda invoke (production) or
	// stderr log (dev mode). Synchronous: blocks for the duration of the
	// Lambda control-plane round-trip (~5-15ms).
	//
	// Call sparingly — once per meaningful action, not per row processed.
	// Errors should be logged, not propagated — billing failures should
	// never fail the handler.
	//
	// Metric names must not contain path separators (/, \), whitespace,
	// dot-segments (..), or null bytes. Panics on invalid name.
	Record(metric string, value float64) error
}

// lambdaInvoker is the subset of lambda.Client used by Client. Makes the
// Lambda invoke path mockable in unit tests.
type lambdaInvoker interface {
	Invoke(ctx context.Context, params *lambda.InvokeInput, optFns ...func(*lambda.Options)) (*lambda.InvokeOutput, error)
}

// Client is the module-level meter client. Created eagerly at Module.New()
// when MS_METER_LAMBDA_ARN is set. Nil in dev mode.
type Client struct {
	lambdaClient lambdaInvoker
	functionARN  string
	logger       *log.Logger // dev-mode stderr sink when lambdaClient is nil
}

// NewFromARN creates a production meter client for the given Lambda function
// ARN. Validates ARN format against arnPattern. Uses the module's default
// AWS IAM role (same pattern as internal/sqs/client.go).
func NewFromARN(ctx context.Context, arn string) (*Client, error) {
	if !arnPattern.MatchString(arn) {
		return nil, fmt.Errorf("mirrorstack/meter: invalid ARN format %q (expected arn:aws:lambda:<region>:<account>:function:<name>)", arn)
	}
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("mirrorstack/meter: load aws config: %w", err)
	}
	return &Client{
		lambdaClient: lambda.NewFromConfig(cfg),
		functionARN:  arn,
	}, nil
}

// NewDev creates a dev-mode meter client that logs Record calls to the given
// logger (typically Module.logger writing to stderr). Returns a non-nil
// Client; Meter.Record is a no-op beyond the log line.
func NewDev(logger *log.Logger) *Client {
	return &Client{logger: logger}
}

// Scope returns a Meter bound to the current request context. The AppID is
// read from auth.Get(ctx) as a hint only — the platform does not trust it.
func (c *Client) Scope(ctx context.Context, moduleID string) Meter {
	appID := ""
	if a := auth.Get(ctx); a != nil {
		appID = a.AppID
	}
	return &scopedMeter{
		ctx:      ctx,
		client:   c,
		moduleID: moduleID,
		appID:    appID,
	}
}

type scopedMeter struct {
	ctx      context.Context
	client   *Client
	moduleID string
	appID    string
}

func (s *scopedMeter) Record(metric string, value float64) error {
	registry.ValidateName("Record", metric)

	// Dev mode: log to stderr and return. The appID may be empty if the
	// context has no auth identity (e.g., internal route, test harness).
	if s.client.lambdaClient == nil {
		s.client.logger.Printf("meter: appID=%q moduleID=%q metric=%q value=%g", s.appID, s.moduleID, metric, value)
		return nil
	}

	event := Event{
		V:              envelopeVersion,
		EventID:        ids.NewUUID(),
		AppIDHint:      s.appID,
		ModuleIDHint:   s.moduleID,
		Metric:         metric,
		Value:          value,
		RecordedAtHint: time.Now().UTC(),
	}
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("mirrorstack/meter: marshal event: %w", err)
	}
	_, err = s.client.lambdaClient.Invoke(s.ctx, &lambda.InvokeInput{
		FunctionName:   &s.client.functionARN,
		InvocationType: types.InvocationTypeEvent, // async fire-and-forget
		Payload:        body,
	})
	if err != nil {
		return fmt.Errorf("mirrorstack/meter: invoke %s: %w", s.client.functionARN, err)
	}
	return nil
}
