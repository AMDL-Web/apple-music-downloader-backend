package api

import (
	"context"
	"time"

	"amdl/internal/domain"
)

// jobEventReplay owns cursor advancement and terminal draining for both job
// transports. The database is authoritative; hub messages are only wakeups.
// Keeping reads separate from writes lets transient store errors retry without
// treating a disconnected client or a failed read as a completed replay.
type jobEventReplay struct {
	read      func(context.Context, int64) ([]domain.Event, error)
	exhausted func(context.Context) bool
}

// eventDelivery contains transport operations. Flush runs once per page, so
// SSE replay does not turn a large backlog into one network flush per event.
type eventDelivery struct {
	write     func(domain.Event) error
	flush     func() error
	keepalive func() error
}

func (replay jobEventReplay) run(ctx context.Context, lastID int64, pending []domain.Event, wake <-chan domain.Event, tick <-chan time.Time, delivery eventDelivery) {
	send := func(page []domain.Event) error {
		for _, ev := range page {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := delivery.write(ev); err != nil {
				return err
			}
			lastID = ev.ID
		}
		if delivery.flush != nil {
			return delivery.flush()
		}
		return nil
	}
	drain := func() (complete bool, err error) {
		for {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			page, err := replay.read(ctx, lastID)
			if err != nil {
				return false, nil // Retry the same cursor on the next wake/tick.
			}
			if err := send(page); err != nil {
				return false, err
			}
			if len(page) < streamEventPageSize {
				return true, nil
			}
		}
	}
	step := func() (stop bool) {
		complete, err := drain()
		if err != nil {
			return true
		}
		if !complete || !replay.exhausted(ctx) {
			return false
		}
		// A terminal hook event can commit between the drain and the pending
		// check. It commits before the finalize/pending marks disappear, so a
		// successful final drain delivers it. A failed read must keep retrying.
		complete, err = drain()
		return complete || err != nil
	}
	if err := send(pending); err != nil || step() {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-wake:
			if !ok || step() {
				return
			}
		case <-tick:
			if step() {
				return
			}
			if delivery.keepalive != nil {
				if err := delivery.keepalive(); err != nil {
					return
				}
			}
		}
	}
}
