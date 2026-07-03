package events

import (
	"sync"
	"testing"
	"time"

	"amdl/internal/domain"
)

func TestHubDeliversToSubscriber(t *testing.T) {
	hub := NewHub()
	ch, cancel := hub.Subscribe("job-1")
	defer cancel()

	hub.Publish(domain.Event{ID: 1, JobID: "job-1", Type: "item_progress"})

	select {
	case ev := <-ch:
		if ev.ID != 1 {
			t.Fatalf("event ID = %d, want 1", ev.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for published event")
	}
}

func TestHubIsolatesByJob(t *testing.T) {
	hub := NewHub()
	ch, cancel := hub.Subscribe("job-1")
	defer cancel()

	// An event for a different job must not reach this subscriber.
	hub.Publish(domain.Event{ID: 1, JobID: "job-2", Type: "item_progress"})

	select {
	case ev := <-ch:
		t.Fatalf("received event for other job: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHubDropsWhenBufferFull(t *testing.T) {
	hub := NewHub()
	// Never read from ch, so once the buffer fills, further publishes are
	// dropped rather than blocking the caller. This mirrors a slow SSE client;
	// the API layer recovers dropped events by re-reading from the store.
	_, cancel := hub.Subscribe("job-1")
	defer cancel()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			hub.Publish(domain.Event{ID: int64(i), JobID: "job-1", Type: "item_progress"})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Publish blocked when subscriber buffer was full")
	}
}

func TestHubUnsubscribeStopsDelivery(t *testing.T) {
	hub := NewHub()
	ch, cancel := hub.Subscribe("job-1")
	cancel()

	// The channel is closed by cancel; a receive returns the zero value with
	// ok=false rather than blocking or panicking.
	if _, ok := <-ch; ok {
		t.Fatal("expected channel to be closed after cancel")
	}
	// Publishing after unsubscribe must not panic on a closed channel.
	hub.Publish(domain.Event{ID: 1, JobID: "job-1", Type: "item_progress"})
}

func TestHubConcurrentSubscribePublishUnsubscribe(t *testing.T) {
	hub := NewHub()
	const jobs = 8
	const perJob = 4

	var wg sync.WaitGroup
	// Concurrent subscribers that immediately drain and then unsubscribe.
	for j := 0; j < jobs; j++ {
		jobID := string(rune('a' + j))
		for s := 0; s < perJob; s++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ch, cancel := hub.Subscribe(jobID)
				go func() {
					for range ch {
					}
				}()
				time.Sleep(time.Millisecond)
				cancel()
			}()
		}
	}
	// Concurrent publishers hammering the same jobs.
	for j := 0; j < jobs; j++ {
		jobID := string(rune('a' + j))
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				hub.Publish(domain.Event{ID: int64(i), JobID: jobID, Type: "item_progress"})
			}
		}()
	}
	wg.Wait()

	// After every subscriber has unsubscribed, the hub must not leak job maps.
	hub.mu.RLock()
	remaining := len(hub.subs)
	hub.mu.RUnlock()
	if remaining != 0 {
		t.Fatalf("hub retained %d job subscription maps after unsubscribe", remaining)
	}
}
