// Package limits provides the process-wide concurrency controls shared by
// Apple catalog requests and media pipeline stages.
package limits

import (
	"context"
	"sync"
)

// Semaphore is a context-aware FIFO concurrency semaphore. A zero or negative
// limit is treated as one so a malformed direct construction can never disable
// progress or accidentally make a resource unlimited.
type Semaphore struct {
	mu      sync.Mutex
	limit   int
	inUse   int
	waiters []*semaphoreWaiter
}

type semaphoreWaiter struct {
	ready chan struct{}
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
	if s.inUse < s.limit && len(s.waiters) == 0 {
		s.inUse++
		s.mu.Unlock()
		return s.releaseFunc(), nil
	}
	w := &semaphoreWaiter{ready: make(chan struct{})}
	s.waiters = append(s.waiters, w)
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
		for i, queued := range s.waiters {
			if queued == w {
				s.waiters = append(s.waiters[:i], s.waiters[i+1:]...)
				s.mu.Unlock()
				return nil, ctx.Err()
			}
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
	if len(s.waiters) == 0 {
		return
	}
	w := s.waiters[0]
	s.waiters = s.waiters[1:]
	s.inUse++
	close(w.ready)
}
