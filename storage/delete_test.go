package storage

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type fakePrefixDeleteAPI struct {
	listOutputs  []*s3.ListObjectsV2Output
	listErr      error
	deleteOutput []*s3.DeleteObjectsOutput
	deleteErr    error
	listInputs   []*s3.ListObjectsV2Input
	deleteInputs []*s3.DeleteObjectsInput
}

func (f *fakePrefixDeleteAPI) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	f.listInputs = append(f.listInputs, in)
	if f.listErr != nil {
		return nil, f.listErr
	}
	if len(f.listOutputs) == 0 {
		return &s3.ListObjectsV2Output{}, nil
	}
	out := f.listOutputs[0]
	f.listOutputs = f.listOutputs[1:]
	return out, nil
}

func (f *fakePrefixDeleteAPI) DeleteObjects(_ context.Context, in *s3.DeleteObjectsInput, _ ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	f.deleteInputs = append(f.deleteInputs, in)
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	if len(f.deleteOutput) == 0 {
		return &s3.DeleteObjectsOutput{}, nil
	}
	out := f.deleteOutput[0]
	f.deleteOutput = f.deleteOutput[1:]
	return out, nil
}

func TestDeletePrefixPaginatesAndScopesKeys(t *testing.T) {
	api := &fakePrefixDeleteAPI{listOutputs: []*s3.ListObjectsV2Output{
		{
			Contents:              []types.Object{{Key: aws.String("apps/app/module/videos/id/a")}, {Key: aws.String("apps/app/module/videos/id/b")}},
			IsTruncated:           aws.Bool(true),
			NextContinuationToken: aws.String("next-page"),
		},
		{Contents: []types.Object{{Key: aws.String("apps/app/module/videos/id/c")}}},
	}}

	if err := deletePrefix(context.Background(), api, "media", "apps/app/module/videos/id/"); err != nil {
		t.Fatal(err)
	}
	if len(api.listInputs) != 2 || aws.ToString(api.listInputs[1].ContinuationToken) != "next-page" {
		t.Fatalf("list inputs=%+v, want two pages with continuation token", api.listInputs)
	}
	for _, in := range api.listInputs {
		if got := aws.ToString(in.Prefix); got != "apps/app/module/videos/id/" {
			t.Fatalf("listed prefix=%q", got)
		}
	}
	if len(api.deleteInputs) != 2 || len(api.deleteInputs[0].Delete.Objects) != 2 || len(api.deleteInputs[1].Delete.Objects) != 1 {
		t.Fatalf("delete inputs=%+v, want one batch per page", api.deleteInputs)
	}
}

func TestDeletePrefixBatchesAtS3Limit(t *testing.T) {
	contents := make([]types.Object, deleteObjectsBatchSize+1)
	for i := range contents {
		contents[i].Key = aws.String("apps/app/module/videos/id/object")
	}
	api := &fakePrefixDeleteAPI{listOutputs: []*s3.ListObjectsV2Output{{Contents: contents}}}
	if err := deletePrefix(context.Background(), api, "media", "apps/app/module/videos/id/"); err != nil {
		t.Fatal(err)
	}
	if len(api.deleteInputs) != 2 || len(api.deleteInputs[0].Delete.Objects) != 1000 || len(api.deleteInputs[1].Delete.Objects) != 1 {
		t.Fatalf("delete batch sizes=%d/%d", len(api.deleteInputs[0].Delete.Objects), len(api.deleteInputs[1].Delete.Objects))
	}
}

func TestDeletePrefixReportsPerObjectErrors(t *testing.T) {
	api := &fakePrefixDeleteAPI{
		listOutputs: []*s3.ListObjectsV2Output{{Contents: []types.Object{{Key: aws.String("apps/app/module/videos/id/locked")}}}},
		deleteOutput: []*s3.DeleteObjectsOutput{{Errors: []types.Error{{
			Key: aws.String("apps/app/module/videos/id/locked"), Code: aws.String("AccessDenied"), Message: aws.String("denied"),
		}}}},
	}
	err := deletePrefix(context.Background(), api, "media", "apps/app/module/videos/id/")
	if err == nil || !strings.Contains(err.Error(), "locked") || !strings.Contains(err.Error(), "AccessDenied") || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("err=%v, want per-object details", err)
	}
}

func TestDeletePrefixMissingPrefixIsIdempotent(t *testing.T) {
	api := &fakePrefixDeleteAPI{listOutputs: []*s3.ListObjectsV2Output{{}}}
	if err := deletePrefix(context.Background(), api, "media", "apps/app/module/videos/missing/"); err != nil {
		t.Fatal(err)
	}
	if len(api.deleteInputs) != 0 {
		t.Fatalf("delete calls=%d, want none", len(api.deleteInputs))
	}
}

func TestDeletePrefixValidatesRelativeDirectory(t *testing.T) {
	c := &Client{presigner: &s3.PresignClient{}, s3Client: &s3.Client{}, bucket: "media", prefix: "apps/app/module/"}
	for _, prefix := range []string{"", "/videos/id/", "../other/", "videos/id", "videos%2Fid/"} {
		if err := c.DeletePrefix(context.Background(), prefix); err == nil {
			t.Errorf("DeletePrefix(%q) returned nil, want validation error", prefix)
		}
	}
	if err := (&Client{}).DeletePrefix(context.Background(), "videos/id/"); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("err=%v, want ErrNoCredential", err)
	}
}
