package limits

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestRequestGateHoldsConcurrencyUntilBodyClose(t *testing.T) {
	t.Parallel()
	gate := NewRequestGate(1, 1000, 1000)
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	})}
	req, _ := http.NewRequest(http.MethodGet, "https://example.test/one", nil)
	first, err := gate.Do(context.Background(), client, req, false)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, "https://example.test/two", nil)
	if _, err := gate.Do(ctx, client, req, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second Do error = %v, want deadline exceeded while first body is open", err)
	}
	if err := first.Body.Close(); err != nil {
		t.Fatal(err)
	}

	req, _ = http.NewRequest(http.MethodGet, "https://example.test/three", nil)
	third, err := gate.Do(context.Background(), client, req, false)
	if err != nil {
		t.Fatalf("Do after body close: %v", err)
	}
	third.Body.Close()
}

func TestRequestGateRateLimitIsContextAware(t *testing.T) {
	t.Parallel()
	gate := NewRequestGate(2, 1, 1)
	release, err := gate.Acquire(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	release()

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	if _, err := gate.Acquire(ctx, true); !errors.Is(err, context.DeadlineExceeded) {
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("rate-limited Acquire error = %v, want context cancellation", err)
		}
	}

	// Requests marked non-rate-limited share concurrency but bypass the token
	// bucket, so artwork and manifests are not serialized by catalog RPS.
	release, err = gate.Acquire(context.Background(), false)
	if err != nil {
		t.Fatalf("non-rate-limited Acquire: %v", err)
	}
	release()
}

func TestRequestGateSharedCooldownAndCancellation(t *testing.T) {
	t.Parallel()
	gate := NewRequestGate(2, 1000, 1000)
	gate.Penalize(50 * time.Millisecond)
	started := time.Now()
	release, err := gate.Acquire(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if elapsed := time.Since(started); elapsed < 40*time.Millisecond {
		t.Fatalf("Acquire waited %v, want shared cooldown", elapsed)
	}

	gate.Penalize(time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := gate.Acquire(ctx, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cooldown Acquire error = %v, want deadline exceeded", err)
	}
}

func TestRequestGateDoesNotPreconsumeRateTokensBehindConcurrency(t *testing.T) {
	t.Parallel()
	gate := NewRequestGate(1, 10, 1)
	first, err := gate.Acquire(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}

	acquired := make(chan time.Time, 2)
	for range 2 {
		go func() {
			release, err := gate.Acquire(context.Background(), true)
			if err != nil {
				return
			}
			acquired <- time.Now()
			release()
		}()
	}
	// Leave both callers queued long enough that the old ordering would have
	// admitted both rate tokens before either could enter the HTTP pool.
	time.Sleep(150 * time.Millisecond)
	first()
	t1 := <-acquired
	t2 := <-acquired
	if spacing := t2.Sub(t1); spacing < 70*time.Millisecond {
		t.Fatalf("queued starts were only %v apart, want token-bucket pacing", spacing)
	}
}

func TestRequestGateDoWith429RetryClosesAndPenalizes(t *testing.T) {
	t.Parallel()
	gate := NewRequestGate(1, 1000, 1000)
	var calls int
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		status := http.StatusOK
		if calls == 1 {
			status = http.StatusTooManyRequests
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("body"))}, nil
	})}
	req, _ := http.NewRequest(http.MethodGet, "https://example.test/manifest", nil)
	started := time.Now()
	resp, err := gate.DoWith429Retry(context.Background(), client, req, false, func(http.Header) time.Duration { return 30 * time.Millisecond })
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if calls != 2 || resp.StatusCode != http.StatusOK {
		t.Fatalf("calls/status = %d/%d, want 2/200", calls, resp.StatusCode)
	}
	if elapsed := time.Since(started); elapsed < 25*time.Millisecond {
		t.Fatalf("retry ignored shared cooldown: %v", elapsed)
	}
}
