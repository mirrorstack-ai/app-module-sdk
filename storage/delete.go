package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const deleteObjectsBatchSize = 1000

// PrefixDeleter is an optional storage capability for removing every object
// below a relative directory prefix. Keeping it separate from Storer preserves
// source compatibility for existing storage implementations and test doubles.
type PrefixDeleter interface {
	DeletePrefix(ctx context.Context, prefix string) error
}

var _ PrefixDeleter = (*Client)(nil)

type prefixDeleteAPI interface {
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	DeleteObjects(context.Context, *s3.DeleteObjectsInput, ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
}

// DeletePrefix removes every object below prefix. prefix is relative to the
// credential's app/module scope and must end in "/" so it cannot match a
// sibling such as "videos/123-backup". A missing prefix succeeds.
func (c *Client) DeletePrefix(ctx context.Context, prefix string) error {
	if err := c.requireCredential(); err != nil {
		return err
	}
	if err := validateDeletePrefix(prefix); err != nil {
		return err
	}
	return deletePrefix(ctx, c.s3Client, c.bucket, c.prefix+prefix)
}

func validateDeletePrefix(prefix string) error {
	if err := validateKey(prefix); err != nil {
		return fmt.Errorf("mirrorstack/storage: invalid delete prefix: %w", err)
	}
	if !strings.HasSuffix(prefix, "/") {
		return fmt.Errorf("mirrorstack/storage: delete prefix must end with '/'")
	}
	return nil
}

func deletePrefix(ctx context.Context, api prefixDeleteAPI, bucket, prefix string) error {
	var (
		continuationToken *string
		objectErrors      []error
	)
	for {
		page, err := api.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: continuationToken,
		})
		if err != nil {
			return fmt.Errorf("mirrorstack/storage: list delete prefix: %w", err)
		}
		if page == nil {
			return fmt.Errorf("mirrorstack/storage: list delete prefix returned no result")
		}

		objects := make([]types.ObjectIdentifier, 0, len(page.Contents))
		for _, object := range page.Contents {
			if object.Key == nil || *object.Key == "" {
				objectErrors = append(objectErrors, errors.New("mirrorstack/storage: list delete prefix returned an object without a key"))
				continue
			}
			objects = append(objects, types.ObjectIdentifier{Key: object.Key})
		}
		for start := 0; start < len(objects); start += deleteObjectsBatchSize {
			end := min(start+deleteObjectsBatchSize, len(objects))
			out, err := api.DeleteObjects(ctx, &s3.DeleteObjectsInput{
				Bucket: aws.String(bucket),
				Delete: &types.Delete{Objects: objects[start:end], Quiet: aws.Bool(true)},
			})
			if err != nil {
				return fmt.Errorf("mirrorstack/storage: delete prefix batch: %w", err)
			}
			if out == nil {
				return fmt.Errorf("mirrorstack/storage: delete prefix batch returned no result")
			}
			for _, objectErr := range out.Errors {
				objectErrors = append(objectErrors, fmt.Errorf(
					"mirrorstack/storage: delete object %q failed (%s): %s",
					aws.ToString(objectErr.Key), aws.ToString(objectErr.Code), aws.ToString(objectErr.Message),
				))
			}
		}

		if !aws.ToBool(page.IsTruncated) {
			break
		}
		next := aws.ToString(page.NextContinuationToken)
		if next == "" || next == aws.ToString(continuationToken) {
			return fmt.Errorf("mirrorstack/storage: list delete prefix returned an invalid continuation token")
		}
		continuationToken = aws.String(next)
	}
	return errors.Join(objectErrors...)
}
