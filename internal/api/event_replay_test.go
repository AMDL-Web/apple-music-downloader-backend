package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"amdl/internal/domain"
)

func TestJobEventReplayRetriesFailedReadsBeforeClosing(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(fmt.Sprintf("read_%d", failAt), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			wake := make(chan domain.Event, 1)
			reads := 0
			var cursors, delivered []int64
			replay := jobEventReplay{
				read: func(_ context.Context, after int64) ([]domain.Event, error) {
					reads++
					cursors = append(cursors, after)
					if reads == failAt {
						wake <- domain.Event{}
						return nil, errors.New("temporary database failure")
					}
					// Simulate a hook event committed during the exhaustion check.
					if reads > failAt && after == 7 {
						return []domain.Event{{ID: 8, Type: "hook_succeeded"}}, nil
					}
					return nil, nil
				},
				exhausted: func(context.Context) bool { return true },
			}
			replay.run(ctx, 6, []domain.Event{{ID: 7}}, wake, nil, eventDelivery{write: func(ev domain.Event) error {
				delivered = append(delivered, ev.ID)
				return nil
			}})
			if !reflect.DeepEqual(delivered, []int64{7, 8}) || ctx.Err() != nil {
				t.Fatalf("delivered %v, cursors %v, context %v", delivered, cursors, ctx.Err())
			}
			if cursors[failAt-1] != cursors[failAt] {
				t.Fatalf("failed read advanced cursor: %v", cursors)
			}
		})
	}
}

func TestJobEventReplayTickRecoversMissedWakeAcrossPages(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	tick := make(chan time.Time, 1)
	readable := false
	terminal := false
	delivered := 0
	replay := jobEventReplay{
		read: func(_ context.Context, after int64) ([]domain.Event, error) {
			if !readable {
				readable = true
				tick <- time.Now() // No hub notification; only the fallback tick.
				return nil, nil
			}
			var page []domain.Event
			for id := after + 1; id <= streamEventPageSize+1 && len(page) < streamEventPageSize; id++ {
				page = append(page, domain.Event{ID: id})
			}
			return page, nil
		},
		exhausted: func(context.Context) bool { return terminal },
	}
	replay.run(ctx, 0, nil, nil, tick, eventDelivery{write: func(ev domain.Event) error {
		delivered++
		if ev.ID != int64(delivered) {
			t.Fatalf("event %d after %d deliveries", ev.ID, delivered)
		}
		terminal = delivered == streamEventPageSize+1
		return nil
	}})
	if delivered != streamEventPageSize+1 || ctx.Err() != nil {
		t.Fatalf("delivered %d, context %v", delivered, ctx.Err())
	}
}

func TestJobEventReplayStopsOnWriteFailure(t *testing.T) {
	for _, initial := range []bool{false, true} {
		t.Run(fmt.Sprintf("initial_%t", initial), func(t *testing.T) {
			writes, reads := 0, 0
			page := []domain.Event{{ID: 1}, {ID: 2}}
			replay := jobEventReplay{
				read:      func(context.Context, int64) ([]domain.Event, error) { reads++; return page, nil },
				exhausted: func(context.Context) bool { t.Fatal("checked exhaustion after write failure"); return false },
			}
			var pending []domain.Event
			if initial {
				pending = page
			}
			replay.run(context.Background(), 0, pending, nil, nil, eventDelivery{write: func(domain.Event) error {
				writes++
				return errors.New("client disconnected")
			}})
			if writes != 1 || (initial && reads != 0) || (!initial && reads != 1) {
				t.Fatalf("writes %d, reads %d", writes, reads)
			}
		})
	}
}

type failingStreamWriter struct {
	*httptest.ResponseRecorder
	err error
}

func TestJobEventReplayStopsOnFlushFailure(t *testing.T) {
	replay := jobEventReplay{
		read: func(context.Context, int64) ([]domain.Event, error) {
			t.Fatal("read continued after failed initial flush")
			return nil, nil
		},
	}
	writes, flushes := 0, 0
	replay.run(context.Background(), 0, []domain.Event{{ID: 1}, {ID: 2}}, nil, nil, eventDelivery{
		write: func(domain.Event) error { writes++; return nil },
		flush: func() error { flushes++; return errors.New("flush failed") },
	})
	if writes != 2 || flushes != 1 {
		t.Fatalf("writes %d, flushes %d; want one flush per page", writes, flushes)
	}
}

func (w failingStreamWriter) Write([]byte) (int, error) { return 0, w.err }
func (w failingStreamWriter) FlushError() error         { return w.err }

func TestSSEPropagatesWriteAndFlushErrors(t *testing.T) {
	want := errors.New("connection closed")
	w := &trackingResponseWriter{ResponseWriter: failingStreamWriter{httptest.NewRecorder(), want}}
	if err := writeSSE(w, domain.Event{ID: 1}); !errors.Is(err, want) {
		t.Fatalf("writeSSE error = %v", err)
	}
	if err := http.NewResponseController(w).Flush(); !errors.Is(err, want) {
		t.Fatalf("Flush error = %v", err)
	}
}
