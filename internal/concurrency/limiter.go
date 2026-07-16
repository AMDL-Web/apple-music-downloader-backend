package concurrency

import (
	"context"
	"sync"
	"time"
)

const limitRefreshInterval = 100 * time.Millisecond

// Limiter bounds an operation across every caller sharing the instance. The
// limit callback is re-read while callers wait, so runtime config changes take
// effect without rebuilding the limiter or cancelling operations already in
// flight.
type Limiter struct {
	limit func() int

	mu      sync.Mutex
	inUse   int
	changed chan struct{}
}

func NewLimiter(limit func() int) *Limiter {
	return &Limiter{limit: limit, changed: make(chan struct{})}
}

func (l *Limiter) Acquire(ctx context.Context) (func(), error) {
	if l == nil {
		return func() {}, nil
	}
	for {
		l.mu.Lock()
		maximum := 1
		if l.limit != nil {
			if configured := l.limit(); configured > 0 {
				maximum = configured
			}
		}
		if l.inUse < maximum {
			l.inUse++
			l.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					l.mu.Lock()
					l.inUse--
					close(l.changed)
					l.changed = make(chan struct{})
					l.mu.Unlock()
				})
			}, nil
		}
		changed := l.changed
		l.mu.Unlock()

		timer := time.NewTimer(limitRefreshInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil, ctx.Err()
		case <-changed:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
			// Re-read the live limit even when no operation has completed. This
			// makes a runtime limit increase observable by existing waiters.
		}
	}
}
