// Package sqs provides a thin wrapper around the AWS SQS SDK for sending
// and receiving task messages. The wrapper exists to centralize queue URL
// handling and to provide a seam for unit-testing without LocalStack.
package sqs

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// Message is a received SQS message with the fields the worker loop needs.
type Message struct {
	Body          string
	ReceiptHandle string
	MessageID     string
	ReceiveCount  int
}

// Client wraps an SQS client with a fixed queue URL.
type Client struct {
	sqs      *sqs.Client
	queueURL string
}

// New creates a Client for the given queue URL. Uses the default AWS config
// (IAM role from the environment — Lambda execution role or ECS task role).
func New(ctx context.Context, queueURL string) (*Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("mirrorstack/sqs: load aws config: %w", err)
	}
	return &Client{
		sqs:      sqs.NewFromConfig(cfg),
		queueURL: queueURL,
	}, nil
}

// Send publishes a message body to the queue. Returns the SQS message ID.
func (c *Client) Send(ctx context.Context, body string) (string, error) {
	out, err := c.sqs.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    &c.queueURL,
		MessageBody: &body,
	})
	if err != nil {
		return "", fmt.Errorf("mirrorstack/sqs: send: %w", err)
	}
	return aws.ToString(out.MessageId), nil
}

// Receive long-polls for up to 1 message with a 20-second wait. Returns
// an empty slice (not an error) when no messages are available.
func (c *Client) Receive(ctx context.Context) ([]Message, error) {
	out, err := c.sqs.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            &c.queueURL,
		MaxNumberOfMessages: 1,
		WaitTimeSeconds:     20,
		AttributeNames:      []types.QueueAttributeName{types.QueueAttributeNameAll},
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{
			types.MessageSystemAttributeNameApproximateReceiveCount,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("mirrorstack/sqs: receive: %w", err)
	}
	msgs := make([]Message, 0, len(out.Messages))
	for _, m := range out.Messages {
		rc := 0
		if v, ok := m.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)]; ok {
			fmt.Sscanf(v, "%d", &rc)
		}
		msgs = append(msgs, Message{
			Body:          aws.ToString(m.Body),
			ReceiptHandle: aws.ToString(m.ReceiptHandle),
			MessageID:     aws.ToString(m.MessageId),
			ReceiveCount:  rc,
		})
	}
	return msgs, nil
}

// Delete removes a message from the queue (ack).
func (c *Client) Delete(ctx context.Context, receiptHandle string) error {
	_, err := c.sqs.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      &c.queueURL,
		ReceiptHandle: &receiptHandle,
	})
	if err != nil {
		return fmt.Errorf("mirrorstack/sqs: delete: %w", err)
	}
	return nil
}

// ChangeVisibility extends or shortens the visibility timeout of a message.
func (c *Client) ChangeVisibility(ctx context.Context, receiptHandle string, timeoutSeconds int32) error {
	_, err := c.sqs.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          &c.queueURL,
		ReceiptHandle:     &receiptHandle,
		VisibilityTimeout: timeoutSeconds,
	})
	if err != nil {
		return fmt.Errorf("mirrorstack/sqs: change visibility: %w", err)
	}
	return nil
}
