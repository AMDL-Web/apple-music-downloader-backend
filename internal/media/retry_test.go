package media

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRetryValueRetriesThenSucceeds(t *testing.T) {
	wantErr := errors.New("temporary")
	var failures []retryFailure
	got, attempts, err := retryValue(context.Background(), 3, func(int) time.Duration { return 0 }, func(attempt int) (string, error) {
		if attempt < 3 {
			return "", wantErr
		}
		return "ok", nil
	}, func(failure retryFailure) {
		failures = append(failures, failure)
	})
	if err != nil || got != "ok" || attempts != 3 {
		t.Fatalf("got value=%q attempts=%d err=%v", got, attempts, err)
	}
	if len(failures) != 2 || !failures[0].WillRetry {
		t.Fatalf("unexpected failures: %+v", failures)
	}
}

func TestRetryValueReportsExhaustion(t *testing.T) {
	wantErr := errors.New("permanent")
	var last retryFailure
	_, attempts, err := retryValue(context.Background(), 3, func(int) time.Duration { return 0 }, func(int) (struct{}, error) {
		return struct{}{}, wantErr
	}, func(failure retryFailure) {
		last = failure
	})
	if !errors.Is(err, wantErr) || attempts != 3 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
	if last.WillRetry || last.Attempt != 3 || last.MaxAttempts != 3 {
		t.Fatalf("unexpected exhaustion state: %+v", last)
	}
}

func TestRetryValueStopsOnNonRetryableError(t *testing.T) {
	wantErr := codecNotFoundError{Codec: "alac"}
	var calls int
	var last retryFailure
	_, attempts, err := retryValue(context.Background(), 2, func(int) time.Duration { return 0 }, func(int) (struct{}, error) {
		calls++
		return struct{}{}, wantErr
	}, func(failure retryFailure) {
		last = failure
	})
	if !errors.As(err, &wantErr) || attempts != 1 || calls != 1 {
		t.Fatalf("attempts=%d calls=%d err=%v", attempts, calls, err)
	}
	if last.WillRetry || last.Attempt != 1 || last.MaxAttempts != 1 {
		t.Fatalf("unexpected non-retryable state: %+v", last)
	}
}

type retryHintError struct{ delay time.Duration }

func (e retryHintError) Error() string             { return "retry later" }
func (e retryHintError) RetryDelay() time.Duration { return e.delay }

func TestRetryValueHonorsLongerDelayHint(t *testing.T) {
	want := 25 * time.Millisecond
	started := time.Now()
	_, attempts, err := retryValue(context.Background(), 2, func(int) time.Duration { return 0 }, func(attempt int) (struct{}, error) {
		if attempt == 1 {
			return struct{}{}, retryHintError{delay: want}
		}
		return struct{}{}, nil
	}, nil)
	if err != nil || attempts != 2 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
	if elapsed := time.Since(started); elapsed < want {
		t.Fatalf("retry elapsed %s, want at least %s", elapsed, want)
	}
}

func TestRetryBackoffAddsBoundedJitter(t *testing.T) {
	for attempt, base := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second} {
		got := retryBackoff(attempt + 1)
		if got < base || got > base+base/2 {
			t.Fatalf("attempt %d backoff=%s, want [%s,%s]", attempt+1, got, base, base+base/2)
		}
	}
}
