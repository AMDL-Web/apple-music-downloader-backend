package limits

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSemaphoreBoundsConcurrentWork(t *testing.T) {
	t.Parallel()
	sem := NewSemaphore(3)
	var active, peak atomic.Int32
	var wg sync.WaitGroup
	for range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := sem.Acquire(context.Background())
			if err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			current := active.Add(1)
			for {
				old := peak.Load()
				if current <= old || peak.CompareAndSwap(old, current) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			active.Add(-1)
			release()
			release() // releases are intentionally idempotent
		}()
	}
	wg.Wait()
	if got := peak.Load(); got != 3 {
		t.Fatalf("peak concurrency = %d, want 3", got)
	}
}

func TestSemaphoreAcquireCancellationDoesNotLeakPermit(t *testing.T) {
	t.Parallel()
	sem := NewSemaphore(1)
	release, err := sem.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := sem.Acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled Acquire error = %v, want deadline exceeded", err)
	}
	release()
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err = sem.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire after canceled waiter: %v", err)
	}
	release()
}

func TestSemaphoreRejectsPreCanceledContext(t *testing.T) {
	t.Parallel()
	sem := NewSemaphore(1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sem.Acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire error = %v, want context.Canceled", err)
	}
	release, err := sem.Acquire(context.Background())
	if err != nil {
		t.Fatalf("permit leaked after pre-cancel: %v", err)
	}
	release()
}
