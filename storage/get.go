package storage

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// MaxGetBytes caps what Get will read into memory. Get exists for small,
// module-owned control files — HLS playlists, manifests, sidecars — and a cap
// keeps a mistyped key from turning a 4 GB source video into an OOM. Streaming
// a large object to a viewer is what PresignGet is for.
const MaxGetBytes = 8 << 20

// ErrObjectTooLarge is returned when an object exceeds MaxGetBytes.
var ErrObjectTooLarge = errors.New("mirrorstack/storage: object exceeds MaxGetBytes")

// Get reads one object's bytes through the S3 API.
//
// WHY THIS EXISTS, and when to use it instead of PresignGet. A presigned URL is
// built for a BROWSER: it is signed against the client-facing endpoint, which in
// dev is the host-published address of the object store. Fetching one from
// inside the module is not merely wasteful (sign, then a second HTTP round trip
// to your own storage) — it is wrong, because the module runs somewhere the
// client-facing address may not resolve. In the dev container it resolves to the
// container itself and the read fails with `dial tcp [::1]:9000: connection
// refused`, which is how this was found: every HLS playlist read 502'd.
//
// Get goes over the S3 API with the module's own credential and the endpoint
// that credential was minted for, so it is correct from wherever the module
// runs. Rule of thumb: bytes YOU need, Get; bytes the VIEWER needs, PresignGet
// or Delivery.
//
// The key is relative to the module's prefix, exactly like every other method
// here, and is validated the same way.
func (c *Client) Get(ctx context.Context, key string) ([]byte, error) {
	if err := c.requireCredential(); err != nil {
		return nil, err
	}
	if err := validateKey(key); err != nil {
		return nil, err
	}
	out, err := c.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(c.prefix + key),
	})
	if err != nil {
		return nil, fmt.Errorf("mirrorstack/storage: get %q failed: %w", key, err)
	}
	defer out.Body.Close()
	// LimitReader at MaxGetBytes+1 so exceeding the cap is DETECTED rather than
	// silently truncated — a truncated playlist is a subtly broken video, which
	// is far worse to debug than a refusal.
	body, err := io.ReadAll(io.LimitReader(out.Body, MaxGetBytes+1))
	if err != nil {
		return nil, fmt.Errorf("mirrorstack/storage: read %q failed: %w", key, err)
	}
	if len(body) > MaxGetBytes {
		return nil, fmt.Errorf("%w: %s", ErrObjectTooLarge, key)
	}
	return body, nil
}
