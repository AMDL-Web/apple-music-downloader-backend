package media

import "context"

const (
	// A 62-track burst against Apple Catalog produced explicit 429 capacity
	// errors, while substantially smaller bursts completed without retries.
	maxConcurrentMetadataRequests = 16
	// 27 concurrent Apple media transfers were stable in the same environment;
	// leave modest headroom without returning to the 62-stream HTTP/2 burst that
	// the CDN reset with INTERNAL_ERROR.
	maxConcurrentMediaDownloads = 32
)

// acquireOperationSlot waits for a phase-specific capacity slot. A nil channel
// means unlimited and keeps small unit-test Downloader literals lightweight.
func acquireOperationSlot(ctx context.Context, slots chan struct{}) (func(), error) {
	if slots == nil {
		return func() {}, nil
	}
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
