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

// acquireInBackground enqueues an Acquire on a full semaphore and returns a
// channel that receives its release func once granted. It waits until the
// queue has grown to wantQueued so callers can build deterministic grant
// orders by enqueueing one waiter at a time.
func acquireInBackground(t *testing.T, sem *Semaphore, ctx context.Context, wantQueued int) <-chan func() {
	t.Helper()
	granted := make(chan func(), 1)
	go func() {
		release, err := sem.Acquire(ctx)
		if err != nil {
			return
		}
		granted <- release
	}()
	deadline := time.Now().Add(time.Second)
	for {
		sem.mu.Lock()
		queued := sem.waiters.Len()
		sem.mu.Unlock()
		if queued >= wantQueued {
			return granted
		}
		if time.Now().After(deadline) {
			t.Fatalf("background Acquire never queued (queue = %d, want %d)", queued, wantQueued)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSemaphoreGrantsContendedPermitsByPriority(t *testing.T) {
	t.Parallel()
	sem := NewSemaphore(1)
	release, err := sem.Acquire(WithPriority(context.Background(), 1))
	if err != nil {
		t.Fatal(err)
	}

	// Enqueue in reverse priority order: a later job first, then an earlier
	// job, then an untagged interactive call. Grants must come back in rank
	// order (interactive, earlier job, later job), not arrival order.
	laterJob := acquireInBackground(t, sem, WithPriority(context.Background(), 30), 1)
	earlierJob := acquireInBackground(t, sem, WithPriority(context.Background(), 20), 2)
	interactive := acquireInBackground(t, sem, context.Background(), 3)

	expectGrant := func(name string, granted <-chan func(), others ...<-chan func()) func() {
		t.Helper()
		select {
		case release := <-granted:
			for _, other := range others {
				select {
				case <-other:
					t.Fatalf("%s: a lower-ranked waiter was granted concurrently", name)
				default:
				}
			}
			return release
		case <-time.After(time.Second):
			t.Fatalf("%s was not granted", name)
			return nil
		}
	}

	release()
	release = expectGrant("interactive", interactive, earlierJob, laterJob)
	release()
	release = expectGrant("earlier job", earlierJob, laterJob)
	release()
	release = expectGrant("later job", laterJob)
	release()
}

func TestSemaphoreKeepsFIFOWithinEqualPriority(t *testing.T) {
	t.Parallel()
	sem := NewSemaphore(1)
	ctx := WithPriority(context.Background(), 7)
	release, err := sem.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first := acquireInBackground(t, sem, ctx, 1)
	second := acquireInBackground(t, sem, ctx, 2)

	release()
	select {
	case release = <-first:
	case <-time.After(time.Second):
		t.Fatal("first equal-priority waiter was not granted")
	}
	select {
	case <-second:
		t.Fatal("second equal-priority waiter overtook the first")
	default:
	}
	release()
	select {
	case release = <-second:
		release()
	case <-time.After(time.Second):
		t.Fatal("second equal-priority waiter was not granted")
	}
}

func TestSemaphorePriorityCancellationDoesNotLeakPermit(t *testing.T) {
	t.Parallel()
	sem := NewSemaphore(1)
	release, err := sem.Acquire(WithPriority(context.Background(), 1))
	if err != nil {
		t.Fatal(err)
	}
	// A high-priority waiter cancels while queued; the pending low-priority
	// waiter must still receive the permit on release.
	cancelCtx, cancel := context.WithCancel(WithPriority(context.Background(), 2))
	canceled := acquireInBackground(t, sem, cancelCtx, 1)
	lowPriority := acquireInBackground(t, sem, WithPriority(context.Background(), 9), 2)
	cancel()
	deadline := time.Now().Add(time.Second)
	for {
		sem.mu.Lock()
		queued := sem.waiters.Len()
		sem.mu.Unlock()
		if queued == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("canceled waiter was not removed from the queue")
		}
		time.Sleep(time.Millisecond)
	}
	release()
	select {
	case release = <-lowPriority:
		release()
	case <-time.After(time.Second):
		t.Fatal("low-priority waiter starved after cancellation")
	}
	select {
	case <-canceled:
		t.Fatal("canceled waiter received a permit")
	default:
	}
}

func TestPriorityFromContext(t *testing.T) {
	t.Parallel()
	if _, ok := PriorityFromContext(context.Background()); ok {
		t.Fatal("untagged context unexpectedly reports a priority")
	}
	if got, ok := PriorityFromContext(WithPriority(context.Background(), 42)); !ok || got != 42 {
		t.Fatalf("PriorityFromContext = (%d, %v), want (42, true)", got, ok)
	}
}
