package events

import (
	"sync"

	"amdl/backend/internal/domain"
)

type Hub struct {
	mu   sync.RWMutex
	subs map[string]map[chan domain.Event]struct{}
}

func NewHub() *Hub {
	return &Hub{subs: map[string]map[chan domain.Event]struct{}{}}
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

func (h *Hub) Publish(ev domain.Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs[ev.JobID] {
		select {
		case ch <- ev:
		default:
		}
	}
}
