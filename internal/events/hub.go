package events

import (
	"sync"

	"amdl/internal/domain"
)

type Hub struct {
	mu sync.RWMutex
	// subs holds per-job subscribers (single-job event streams). allSubs holds
	// overview subscribers, which receive only the milestone events that change
	// the GET /downloads list-level view (domain.IsOverviewMilestone).
	subs    map[string]map[chan domain.Event]struct{}
	allSubs map[chan domain.Event]struct{}
}

func NewHub() *Hub {
	return &Hub{
		subs:    map[string]map[chan domain.Event]struct{}{},
		allSubs: map[chan domain.Event]struct{}{},
	}
}

func (h *Hub) Subscribe(jobID string) (<-chan domain.Event, func()) {
	ch := make(chan domain.Event, 64)
	h.mu.Lock()
	if h.subs[jobID] == nil {
		h.subs[jobID] = map[chan domain.Event]struct{}{}
	}
	h.subs[jobID][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		if subs := h.subs[jobID]; subs != nil {
			delete(subs, ch)
			if len(subs) == 0 {
				delete(h.subs, jobID)
			}
		}
		close(ch)
		h.mu.Unlock()
	}
}

// SubscribeAll registers an overview subscriber that receives every milestone
// event across all jobs (see domain.IsOverviewMilestone). Like Subscribe, the
// channel is a best-effort wake signal — a full buffer drops events, and the
// overview handler re-derives state from the store, so nothing is lost as long
// as the milestone was persisted (job_deleted, which isn't, is the one type a
// dropped wake can miss; clients recover it on reconnect via GET /downloads).
func (h *Hub) SubscribeAll() (<-chan domain.Event, func()) {
	ch := make(chan domain.Event, 64)
	h.mu.Lock()
	h.allSubs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.allSubs, ch)
		close(ch)
		h.mu.Unlock()
	}
}

func (h *Hub) Publish(ev domain.Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs[ev.JobID] {
		select {
		case ch <- ev:
		default:
		}
	}
	// Overview subscribers only care about list-level milestones, so filtering
	// here keeps the far more frequent per-item detail events from waking them.
	if domain.IsOverviewMilestone(ev.Type) {
		for ch := range h.allSubs {
			select {
			case ch <- ev:
			default:
			}
		}
	}
}
