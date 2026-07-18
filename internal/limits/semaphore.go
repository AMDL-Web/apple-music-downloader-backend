// Package limits provides the process-wide concurrency controls shared by
// Apple catalog requests and media pipeline stages.
package limits

import (
	"container/heap"
	"context"
	"math"
	"sync"
)

type priorityKey struct{}
type subpriorityKey struct{}

// WithPriority tags ctx so every pool Acquire made through it competes at the
// given rank: lower values win contended permits first, equal values keep
// their arrival order. The jobs manager stamps each running job's context
// with its submission time, so when pools are saturated capacity flows to the
// oldest unfinished job and jobs tend to complete one at a time instead of
// interleaving. Priority only decides who is admitted next — idle capacity is
// always granted immediately, so a preferred job that cannot saturate a pool
// never starves later jobs of leftover throughput.
func WithPriority(ctx context.Context, priority int64) context.Context {
	return context.WithValue(ctx, priorityKey{}, priority)
}

// WithSubpriority adds a tie-breaker beneath the primary priority. Lower
// values win when two callers have the same primary priority. The media
// scheduler uses this for track indexes so earlier tracks in the same job get
// contended pool capacity first without overtaking tracks from an older job.
func WithSubpriority(ctx context.Context, subpriority int64) context.Context {
	return context.WithValue(ctx, subpriorityKey{}, subpriority)
}

// PriorityFromContext reports the rank WithPriority attached, if any.
func PriorityFromContext(ctx context.Context) (int64, bool) {
	priority, ok := ctx.Value(priorityKey{}).(int64)
	return priority, ok
}

// SubpriorityFromContext reports the tie-breaker WithSubpriority attached, if
// any. It is kept separate from PriorityFromContext so existing callers that
// inspect the job-level priority retain the same contract.
func SubpriorityFromContext(ctx context.Context) (int64, bool) {
	subpriority, ok := ctx.Value(subpriorityKey{}).(int64)
	return subpriority, ok
}

type contextRank struct {
	priority    int64
	subpriority int64
}

// contextPriority resolves the rank an Acquire competes at. Untagged contexts
// (interactive API calls such as URL validation and quality probes) outrank
// every job, so sparse foreground requests never queue behind a bulk
// download's backlog.
func contextPriority(ctx context.Context) contextRank {
	priority, ok := PriorityFromContext(ctx)
	if !ok {
		return contextRank{priority: math.MinInt64, subpriority: math.MinInt64}
	}
	subpriority, _ := SubpriorityFromContext(ctx)
	return contextRank{priority: priority, subpriority: subpriority}
}

// Semaphore is a context-aware concurrency semaphore whose contended permits
// are granted by primary priority, subpriority, then FIFO arrival order. A zero
// or negative limit is treated as one so a malformed direct construction can
// never disable progress or accidentally make a resource unlimited.
type Semaphore struct {
	mu      sync.Mutex
	limit   int
	inUse   int
	arrival uint64
	waiters waiterHeap
}

type semaphoreWaiter struct {
	ready    chan struct{}
	priority int64
	// subpriority orders callers within one primary priority (for example,
	// tracks within one job) before arrival order is considered.
	subpriority int64
	arrival     uint64
	// index is the waiter's position in the heap, or -1 once it has been
	// granted (removed from the heap). Guarded by Semaphore.mu.
	index int
}

// waiterHeap orders waiters by primary priority, subpriority, then arrival.
type waiterHeap []*semaphoreWaiter

func (h waiterHeap) Len() int { return len(h) }

func (h waiterHeap) Less(i, j int) bool {
	if h[i].priority != h[j].priority {
		return h[i].priority < h[j].priority
	}
	if h[i].subpriority != h[j].subpriority {
		return h[i].subpriority < h[j].subpriority
	}
	return h[i].arrival < h[j].arrival
}

func (h waiterHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *waiterHeap) Push(x any) {
	w := x.(*semaphoreWaiter)
	w.index = len(*h)
	*h = append(*h, w)
}

func (h *waiterHeap) Pop() any {
	old := *h
	n := len(old)
	w := old[n-1]
	old[n-1] = nil
	w.index = -1
	*h = old[:n-1]
	return w
}

func NewSemaphore(limit int) *Semaphore {
	if limit <= 0 {
		limit = 1
	}
	return &Semaphore{limit: limit}
}

// Acquire waits for one permit and returns an idempotent release function.
// Canceled waiters are removed without consuming capacity.
func (s *Semaphore) Acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.inUse < s.limit && s.waiters.Len() == 0 {
		s.inUse++
		s.mu.Unlock()
		return s.releaseFunc(), nil
	}
	rank := contextPriority(ctx)
	w := &semaphoreWaiter{
		ready: make(chan struct{}), priority: rank.priority, subpriority: rank.subpriority, arrival: s.arrival,
	}
	s.arrival++
	heap.Push(&s.waiters, w)
	s.mu.Unlock()

	select {
	case <-w.ready:
		if err := ctx.Err(); err != nil {
			s.mu.Lock()
			s.releaseLocked()
			s.mu.Unlock()
			return nil, err
		}
		return s.releaseFunc(), nil
	case <-ctx.Done():
		s.mu.Lock()
		if w.index >= 0 {
			heap.Remove(&s.waiters, w.index)
			s.mu.Unlock()
			return nil, ctx.Err()
		}
		// The waiter was granted concurrently with cancellation. Return its
		// permit before reporting the context error.
		s.releaseLocked()
		s.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (s *Semaphore) releaseFunc() func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.inUse == 0 {
				panic("limits.Semaphore: release without acquire")
			}
			s.releaseLocked()
		})
	}
}

func (s *Semaphore) releaseLocked() {
	s.inUse--
	if s.waiters.Len() == 0 {
		return
	}
	w := heap.Pop(&s.waiters).(*semaphoreWaiter)
	s.inUse++
	close(w.ready)
}
