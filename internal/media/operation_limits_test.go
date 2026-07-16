package media

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAcquireOperationSlotRespectsCapacityAndCancellation(t *testing.T) {
	slots := make(chan struct{}, 1)
	release, err := acquireOperationSlot(context.Background(), slots)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := acquireOperationSlot(ctx, slots); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second acquire error=%v, want deadline exceeded", err)
	}

	release()
	secondRelease, err := acquireOperationSlot(context.Background(), slots)
	if err != nil {
		t.Fatal(err)
	}
	secondRelease()
}
