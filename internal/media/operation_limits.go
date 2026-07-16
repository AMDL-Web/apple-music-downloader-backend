package media

import (
	"context"

	"amdl/internal/concurrency"
)

// acquireOperationSlot waits for a phase-specific capacity slot. A nil limiter
// means unlimited and keeps small unit-test Downloader literals lightweight.
func acquireOperationSlot(ctx context.Context, limiter *concurrency.Limiter) (func(), error) {
	if limiter == nil {
		return func() {}, nil
	}
	return limiter.Acquire(ctx)
}
