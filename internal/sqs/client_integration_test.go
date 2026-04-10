//go:build integration

package sqs

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	sqssdk "github.com/aws/aws-sdk-go-v2/service/sqs"
)

// sqsEndpoint returns the SQS-compatible endpoint, defaulting to ElasticMQ on localhost:9324.
func sqsEndpoint() string {
	if ep := os.Getenv("SQS_ENDPOINT"); ep != "" {
		return ep
	}
	return "http://localhost:9324"
}

// skipIfNoSQS skips the test if ElasticMQ is not reachable.
func skipIfNoSQS(t *testing.T) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", "localhost:9324", 2*time.Second)
	if err != nil {
		t.Skip("ElasticMQ not available, skipping SQS integration test")
	}
	conn.Close()
}

// testAWSConfig returns an AWS config pointing at ElasticMQ with fake credentials.
func testAWSConfig(ctx context.Context) (aws.Config, error) {
	return config.LoadDefaultConfig(ctx,
		config.WithRegion("us-east-1"),
		config.WithBaseEndpoint(sqsEndpoint()),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
}

// createTestQueue creates an SQS queue in ElasticMQ and returns its URL.
func createTestQueue(t *testing.T, ctx context.Context) string {
	t.Helper()

	cfg, err := testAWSConfig(ctx)
	if err != nil {
		t.Fatalf("load aws config: %v", err)
	}

	client := sqssdk.NewFromConfig(cfg)
	queueName := "test-queue-" + t.Name()
	out, err := client.CreateQueue(ctx, &sqssdk.CreateQueueInput{
		QueueName: &queueName,
	})
	if err != nil {
		t.Fatalf("create queue: %v", err)
	}
	t.Cleanup(func() {
		client.DeleteQueue(context.Background(), &sqssdk.DeleteQueueInput{
			QueueUrl: out.QueueUrl,
		})
	})
	return aws.ToString(out.QueueUrl)
}

func TestIntegration_SendReceiveDelete(t *testing.T) {
	skipIfNoSQS(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Set fake credentials BEFORE any AWS SDK calls
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ENDPOINT_URL", sqsEndpoint())

	queueURL := createTestQueue(t, ctx)

	c, err := New(ctx, queueURL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Send
	body := `{"taskId":"t1","name":"work","payload":{}}`
	msgID, err := c.Send(ctx, body)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if msgID == "" {
		t.Error("Send should return a non-empty message ID")
	}

	// Receive
	msgs, err := c.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("Receive returned 0 messages, expected 1")
	}

	msg := msgs[0]
	if msg.Body != body {
		t.Errorf("body = %q, want %q", msg.Body, body)
	}
	if msg.MessageID == "" {
		t.Error("MessageID should be set")
	}

	// Verify JSON roundtrip
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(msg.Body), &parsed); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}

	// Delete (ack)
	if err := c.Delete(ctx, msg.ReceiptHandle); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Verify queue is empty after delete
	msgs2, err := c.Receive(ctx)
	if err != nil {
		t.Fatalf("second Receive: %v", err)
	}
	if len(msgs2) != 0 {
		t.Errorf("queue should be empty after delete, got %d messages", len(msgs2))
	}
}

func TestIntegration_ChangeVisibility(t *testing.T) {
	skipIfNoSQS(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ENDPOINT_URL", sqsEndpoint())

	queueURL := createTestQueue(t, ctx)

	c, err := New(ctx, queueURL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = c.Send(ctx, `{"name":"test"}`)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	msgs, err := c.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected 1 message")
	}

	if err := c.ChangeVisibility(ctx, msgs[0].ReceiptHandle, 60); err != nil {
		t.Fatalf("ChangeVisibility: %v", err)
	}

	c.Delete(ctx, msgs[0].ReceiptHandle)
}
