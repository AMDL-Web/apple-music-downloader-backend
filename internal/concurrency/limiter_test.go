package concurrency

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestLimiterRespectsCapacityAndCancellation(t *testing.T) {
	limiter := NewLimiter(func() int { return 1 })
	release, err := limiter.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := limiter.Acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second acquire error=%v, want deadline exceeded", err)
	}
	release()
}

func TestLimiterObservesRuntimeIncrease(t *testing.T) {
	var maximum atomic.Int64
	maximum.Store(1)
	limiter := NewLimiter(func() int { return int(maximum.Load()) })
	release, err := limiter.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	acquired := make(chan func(), 1)
	go func() {
		release, err := limiter.Acquire(context.Background())
		if err == nil {
			acquired <- release
		}
	}()
	maximum.Store(2)
	select {
	case secondRelease := <-acquired:
		secondRelease()
	case <-time.After(time.Second):
		t.Fatal("waiter did not observe increased limit")
	}
}

func TestLimiterNormalizesNonPositiveLimitAndReleaseIsIdempotent(t *testing.T) {
	limiter := NewLimiter(func() int { return 0 })
	release, err := limiter.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
	release()
	secondRelease, err := limiter.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	secondRelease()
}
