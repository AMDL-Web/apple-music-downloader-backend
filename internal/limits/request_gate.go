package limits

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// RequestGate combines a shared request-concurrency limit, a token bucket for
// rate-limited endpoints, and a process-wide cooldown after an upstream 429.
// The gate is safe for concurrent use.
type RequestGate struct {
	concurrency *Semaphore
	rate        *rate.Limiter

	cooldownMu    sync.Mutex
	cooldownUntil time.Time
}

func NewRequestGate(maxParallel, requestsPerSecond, burst int) *RequestGate {
	if requestsPerSecond <= 0 {
		requestsPerSecond = 1
	}
	if burst <= 0 {
		burst = 1
	}
	return &RequestGate{
		concurrency: NewSemaphore(maxParallel),
		rate:        rate.NewLimiter(rate.Limit(requestsPerSecond), burst),
	}
}

// Acquire waits for cooldown and shared concurrency before optional
// token-bucket admission. Taking the concurrency slot first keeps the token
// admission adjacent to the actual HTTP start: callers queued behind a full
// pool cannot pre-consume tokens and later escape the configured burst.
// The returned release function is idempotent.
func (g *RequestGate) Acquire(ctx context.Context, rateLimited bool) (func(), error) {
	for {
		if err := g.waitCooldown(ctx); err != nil {
			return nil, err
		}
		release, err := g.concurrency.Acquire(ctx)
		if err != nil {
			return nil, err
		}

		// A 429 may have extended the cooldown while this caller waited for
		// concurrency. Do not let queued requests escape that new penalty.
		if g.coolingDown() {
			release()
			continue
		}
		if rateLimited {
			if err := g.rate.Wait(ctx); err != nil {
				release()
				return nil, err
			}
			// A 429 may also arrive while the token bucket was pacing us.
			if g.coolingDown() {
				release()
				continue
			}
		}
		return release, nil
	}
}

// Do admits an HTTP request and holds its concurrency permit until the
// response body is closed. Transport failures release immediately.
func (g *RequestGate) Do(ctx context.Context, client *http.Client, req *http.Request, rateLimited bool) (*http.Response, error) {
	release, err := g.Acquire(ctx, rateLimited)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		release()
		return nil, err
	}
	if resp.Body == nil {
		release()
		return resp, nil
	}
	resp.Body = &releaseOnClose{ReadCloser: resp.Body, release: release}
	return resp, nil
}

// DoWith429Retry behaves like Do, but a 429 penalizes every caller through
// the shared cooldown and is retried once. The first response body is closed
// before waiting so it cannot retain a concurrency permit or transport
// connection. retryDelay is injectable for callers' deterministic tests.
func (g *RequestGate) DoWith429Retry(ctx context.Context, client *http.Client, req *http.Request, rateLimited bool, retryDelay func(http.Header) time.Duration) (*http.Response, error) {
	if retryDelay == nil {
		retryDelay = DefaultRetryDelay
	}
	for attempt := 0; ; attempt++ {
		resp, err := g.Do(ctx, client, req, rateLimited)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusTooManyRequests {
			return resp, nil
		}
		g.Penalize(retryDelay(resp.Header))
		if attempt == 1 {
			return resp, nil
		}
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}
}

// DefaultRetryDelay parses Retry-After as either delta-seconds or an HTTP
// date. Apple sometimes omits it, so the fallback is one second plus up to
// 250ms of jitter to keep concurrent retries from forming another burst.
func DefaultRetryDelay(header http.Header) time.Duration {
	if raw := strings.TrimSpace(header.Get("Retry-After")); raw != "" {
		if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds >= 0 {
			return time.Duration(seconds) * time.Second
		}
		if at, err := http.ParseTime(raw); err == nil {
			if delay := time.Until(at); delay > 0 {
				return delay
			}
			return 0
		}
	}
	jitter := time.Duration(time.Now().UnixNano() % int64(250*time.Millisecond))
	return time.Second + jitter
}

// Penalize extends the shared cooldown. A shorter concurrent penalty never
// reduces a longer one already in force.
func (g *RequestGate) Penalize(delay time.Duration) {
	if delay <= 0 {
		return
	}
	until := time.Now().Add(delay)
	g.cooldownMu.Lock()
	if until.After(g.cooldownUntil) {
		g.cooldownUntil = until
	}
	g.cooldownMu.Unlock()
}

func (g *RequestGate) waitCooldown(ctx context.Context) error {
	for {
		g.cooldownMu.Lock()
		until := g.cooldownUntil
		g.cooldownMu.Unlock()
		delay := time.Until(until)
		if delay <= 0 {
			return nil
		}
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}

func (g *RequestGate) coolingDown() bool {
	g.cooldownMu.Lock()
	defer g.cooldownMu.Unlock()
	return time.Now().Before(g.cooldownUntil)
}

type releaseOnClose struct {
	io.ReadCloser
	release func()
	once    sync.Once
}

func (b *releaseOnClose) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.release)
	return err
}
