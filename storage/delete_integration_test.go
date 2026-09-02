package storage

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// TestDeletePrefixAgainstDevStorage exercises the complete local security
// path: parent credentials -> STS session policy -> scoped list/delete. It is
// opt-in because ordinary SDK tests must not require a running MinIO service.
func TestDeletePrefixAgainstDevStorage(t *testing.T) {
	if os.Getenv("MS_STORAGE_INTEGRATION") != "1" {
		t.Skip("set MS_STORAGE_INTEGRATION=1 with the module dev MinIO running")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg, err := DevConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	minter, err := NewDevMinter(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	const (
		appA   = "11111111-1111-1111-1111-111111111111"
		appB   = "22222222-2222-2222-2222-222222222222"
		module = "storage_integration"
		videoA = "videos/delete-me/"
		videoB = "videos/keep-me/"
	)
	clientFor := func(appID string) *Client {
		credential, mintErr := minter.Mint(ctx, appID, module)
		if mintErr != nil {
			t.Fatal(mintErr)
		}
		client, clientErr := NewFromCredentialForDev(credential, cfg)
		if clientErr != nil {
			t.Fatal(clientErr)
		}
		return client
	}
	first, second := clientFor(appA), clientFor(appB)
	put := func(client *Client, relative string) {
		_, putErr := client.s3Client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(client.bucket),
			Key:    aws.String(client.prefix + relative),
			Body:   bytes.NewReader([]byte("integration")),
		})
		if putErr != nil {
			t.Fatal(putErr)
		}
	}
	put(first, videoA+"master.m3u8")
	put(first, videoB+"source.mp4")
	put(second, videoA+"other-app.mp4")
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_ = first.DeletePrefix(cleanupCtx, videoA)
		_ = first.DeletePrefix(cleanupCtx, videoB)
		_ = second.DeletePrefix(cleanupCtx, videoA)
	})

	if err := first.DeletePrefix(ctx, videoA); err != nil {
		t.Fatal(err)
	}
	assertCount := func(client *Client, prefix string, want int) {
		out, listErr := client.s3Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: aws.String(client.bucket),
			Prefix: aws.String(client.prefix + prefix),
		})
		if listErr != nil {
			t.Fatal(listErr)
		}
		if got := len(out.Contents); got != want {
			t.Fatalf("objects below %s%s = %d, want %d", client.prefix, prefix, got, want)
		}
	}
	assertCount(first, videoA, 0)
	assertCount(first, videoB, 1)
	assertCount(second, videoA, 1)

	if _, err := first.s3Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(first.bucket),
		Prefix: aws.String(second.prefix),
	}); err == nil {
		t.Fatal("first app credential listed the second app prefix")
	}
}
