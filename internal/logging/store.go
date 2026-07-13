package logging

import (
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"
)

// Entry is the stable structured representation exposed by the log API.
type Entry struct {
	Sequence   uint64         `json:"sequence"`
	Time       time.Time      `json:"time"`
	Level      string         `json:"level"`
	Message    string         `json:"message"`
	Source     string         `json:"source,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// Filter selects retained records. After is an exclusive sequence cursor.
type Filter struct {
	After     uint64
	Levels    []string
	Component string
	RequestID string
	JobID     string
	Query     string
	Since     time.Time
	Until     time.Time
	Limit     int
}

type Page struct {
	Entries        []Entry
	NextCursor     uint64
	OldestSequence uint64
	Truncated      bool
}

type Store struct {
	mu       sync.RWMutex
	capacity int
	next     uint64
	entries  []Entry
	subs     map[chan Entry]Filter
}

func NewStore(capacity int) *Store {
	return &Store{capacity: capacity, entries: make([]Entry, 0, capacity), subs: map[chan Entry]Filter{}}
}

func (s *Store) append(entry Entry) {
	s.mu.Lock()
	s.next++
	entry.Sequence = s.next
	if s.capacity > 0 {
		if len(s.entries) == s.capacity {
			copy(s.entries, s.entries[1:])
			s.entries[len(s.entries)-1] = entry
		} else {
			s.entries = append(s.entries, entry)
		}
	}
	for ch, filter := range s.subs {
		if !matches(entry, filter) {
			continue
		}
		select {
		case ch <- entry:
		default:
			// Force a slow client to reconnect with its last received sequence;
			// silently dropping here would leave an undetectable hole in its stream.
			delete(s.subs, ch)
			close(ch)
		}
	}
	s.mu.Unlock()
}

// List returns matching records in chronological order. A first request tails
// the newest Limit records; a cursor request pages forward from After without
// skipping matches. Truncated reports that After predates the retained ring.
func (s *Store) List(filter Filter) Page {
	s.mu.RLock()
	defer s.mu.RUnlock()
	page := Page{NextCursor: s.next}
	if len(s.entries) > 0 {
		page.OldestSequence = s.entries[0].Sequence
		page.Truncated = filter.After > 0 && filter.After < page.OldestSequence-1
	} else if filter.After > 0 && filter.After < s.next {
		page.Truncated = true
	}
	entries := make([]Entry, 0, len(s.entries))
	for _, entry := range s.entries {
		if matches(entry, filter) {
			entries = append(entries, entry)
		}
	}
	if filter.Limit > 0 && len(entries) > filter.Limit {
		if filter.After > 0 {
			entries = entries[:filter.Limit]
			page.NextCursor = entries[len(entries)-1].Sequence
		} else {
			entries = entries[len(entries)-filter.Limit:]
		}
	}
	page.Entries = slices.Clone(entries)
	return page
}

// Subscribe atomically returns the matching retained backlog and registers a
// best-effort live subscriber, so no record can fall between replay and live.
func (s *Store) Subscribe(filter Filter) ([]Entry, <-chan Entry, func()) {
	s.mu.Lock()
	backlog := make([]Entry, 0, min(len(s.entries), filter.Limit))
	for _, entry := range s.entries {
		if matches(entry, filter) {
			backlog = append(backlog, entry)
		}
	}
	if filter.After == 0 && filter.Limit > 0 && len(backlog) > filter.Limit {
		backlog = backlog[len(backlog)-filter.Limit:]
	}
	ch := make(chan Entry, 256)
	s.subs[ch] = filter
	s.mu.Unlock()

	return slices.Clone(backlog), ch, func() {
		s.mu.Lock()
		if _, ok := s.subs[ch]; ok {
			delete(s.subs, ch)
			close(ch)
		}
		s.mu.Unlock()
	}
}

func matches(entry Entry, filter Filter) bool {
	if entry.Sequence <= filter.After || (!filter.Since.IsZero() && entry.Time.Before(filter.Since)) || (!filter.Until.IsZero() && entry.Time.After(filter.Until)) {
		return false
	}
	if len(filter.Levels) > 0 && !slices.Contains(filter.Levels, entry.Level) {
		return false
	}
	if filter.Component != "" && attributeString(entry, "component") != filter.Component {
		return false
	}
	if filter.RequestID != "" && attributeString(entry, "request_id") != filter.RequestID {
		return false
	}
	if filter.JobID != "" && attributeString(entry, "job_id") != filter.JobID {
		return false
	}
	if filter.Query != "" {
		haystack := strings.ToLower(entry.Message + " " + attributesText(entry.Attributes))
		if !strings.Contains(haystack, strings.ToLower(filter.Query)) {
			return false
		}
	}
	return true
}

func attributeString(entry Entry, key string) string {
	value, _ := entry.Attributes[key].(string)
	return value
}

func attributesText(attrs map[string]any) string {
	raw, _ := json.Marshal(attrs)
	return string(raw)
}
