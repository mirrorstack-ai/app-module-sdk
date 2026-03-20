package storage

import (
	"context"

	"github.com/mirrorstack-ai/app-module-sdk/meter"
)

// trackOp records a storage operation metric if a meter is in the context.
func trackOp(ctx context.Context, metric string) {
	if m := meter.FromContext(ctx); m != nil {
		m.Track(metric, 1)
	}
}
