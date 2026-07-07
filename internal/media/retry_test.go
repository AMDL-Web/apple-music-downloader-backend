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
