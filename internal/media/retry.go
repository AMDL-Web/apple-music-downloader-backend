package media

import (
	"context"
	"errors"
	"time"
)

type retryFailure struct {
	Attempt     int
	MaxAttempts int
	Delay       time.Duration
	WillRetry   bool
	Err         error
}

type nonRetryable interface {
	NonRetryable() bool
}

func isNonRetryableError(err error) bool {
	var marker nonRetryable
	return errors.As(err, &marker) && marker.NonRetryable()
}

func maxAttempts(retries int) int {
	if retries < 0 {
		retries = 0
	}
	return retries + 1
}

func retryBackoff(attempt int) time.Duration {
	shift := attempt - 1
	if shift < 0 {
		shift = 0
	}
	if shift > 3 {
		shift = 3
	}
	return time.Second * time.Duration(1<<shift)
}

func retryValue[T any](
	ctx context.Context,
	retries int,
	delayFor func(int) time.Duration,
	op func(attempt int) (T, error),
	onFailure func(retryFailure),
) (T, int, error) {
	var zero T
	maximum := maxAttempts(retries)
	for attempt := 1; attempt <= maximum; attempt++ {
		value, err := op(attempt)
		if err == nil {
			return value, attempt, nil
		}
		if ctx.Err() != nil {
			return zero, attempt, err
		}
		nonRetryable := isNonRetryableError(err)
		willRetry := attempt < maximum && !nonRetryable
		reportedMaximum := maximum
		if nonRetryable {
			reportedMaximum = attempt
		}
		delay := time.Duration(0)
		if willRetry {
			delay = delayFor(attempt)
		}
		if onFailure != nil {
			onFailure(retryFailure{Attempt: attempt, MaxAttempts: reportedMaximum, Delay: delay, WillRetry: willRetry, Err: err})
		}
		if !willRetry {
			return zero, attempt, err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return zero, attempt, ctx.Err()
		case <-timer.C:
		}
	}
	return zero, maximum, nil
}
