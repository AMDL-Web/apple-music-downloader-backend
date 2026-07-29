package media

import (
	"context"
	"time"

	"amdl/internal/config"
	"amdl/internal/domain"
	"amdl/internal/jobs"
)

// eventItemProgress is the one high-frequency event type: the per-item
// download/decrypt meter feed. Every other item event is a one-shot pipeline
// milestone.
const eventItemProgress = "item_progress"

// progressGate coalesces one item's item_progress feed down to at most one
// event per download.progress_event_interval_ms.
//
// The percent-rounding gate in set() bounds an item to ~101 emissions per meter
// per attempt, but puts no floor on their spacing: a track that transfers in a
// few seconds fires all ~101 in those few seconds, and a 20-track album at
// max_parallel_downloads runs dozens of those meters at once. Each emission is
// a SQLite UPDATE plus an INSERT plus a hub broadcast, and every persisted row
// is then re-read and written to every subscriber — amdl-portal fans it out per
// user (slow clients get dropped), amdl-ios-gateway turns it into APNs Live
// Activity traffic, and the web frontend re-renders the job list. So the fix is
// a time floor, applied at the point of persistence so that every consumer,
// live or replaying from last_event_id, sees the identical reduced stream.
//
// What is rate-limited is only meter movement *within* one state. Three rules
// keep that from losing anything:
//
//   - Status changes and pipeline-stage completions bypass the gate entirely
//     and are emitted immediately (see set in downloader.go), so no terminal or
//     transition event is ever delayed.
//   - Every other item event carries a full JobItem snapshot, so a held meter
//     value can never be the last word: Event flushes the held update ahead of
//     any non-progress event, which puts the final meter value on the wire
//     immediately before item_completed / item_failed / item_skipped.
//   - Because the flush happens *before* the superseding event, event ids stay
//     monotonic and in causal order, and a client replaying from last_event_id
//     reads exactly what a live client saw.
//
// Not safe for concurrent use, and deliberately so: one gate belongs to one
// item, and an item is driven start to finish by a single goroutine (the media
// producer runs inline, never in a goroutine of its own). There is no
// background timer for the same reason — a timer firing after the item's
// terminal event would rewind the item's state on every client.
type progressGate struct {
	// The embedded Reporter carries every method through untouched; only
	// Event is intercepted.
	jobs.Reporter

	interval time.Duration
	now      func() time.Time
	lastEmit time.Time
	held     bool
	// emit publishes the item's current state as an item_progress event. Owned
	// by processTrackWithMetadata, which is where the live item value lives.
	emit func()
}

func newProgressGate(base jobs.Reporter, interval time.Duration) *progressGate {
	return &progressGate{Reporter: base, interval: interval, now: time.Now}
}

// progressEventInterval is the configured coalescing interval; zero (or
// negative, which Validate rejects and clampDownloadLimits repairs) disables
// time-based coalescing and restores the pure percent-rounding behaviour.
func progressEventInterval(cfg config.Config) time.Duration {
	if cfg.Download.ProgressEventIntervalMS <= 0 {
		return 0
	}
	return time.Duration(cfg.Download.ProgressEventIntervalMS) * time.Millisecond
}

// allow reports whether a progress-only update may go out now. The first
// update of an item always passes, so a meter starts moving without waiting
// out an interval.
func (g *progressGate) allow() bool {
	if g.interval <= 0 || g.lastEmit.IsZero() {
		return true
	}
	return g.now().Sub(g.lastEmit) >= g.interval
}

// hold records that a progress update was suppressed, so the value is still
// published if nothing supersedes it.
func (g *progressGate) hold() { g.held = true }

// emitted is called by the emit function once an item_progress has actually
// been published, whether the gate allowed it or a flush forced it.
func (g *progressGate) emitted() {
	g.lastEmit = g.now()
	g.held = false
}

// flush publishes a suppressed progress update, if one is pending.
func (g *progressGate) flush() {
	if !g.held || g.emit == nil {
		return
	}
	g.held = false
	g.emit()
}

// Event publishes any held meter value before the event that supersedes it, so
// the last progress value before a state change is never dropped and never
// arrives after the state change it preceded.
func (g *progressGate) Event(ctx context.Context, ev domain.Event) error {
	if ev.Type != eventItemProgress {
		g.flush()
	}
	return g.Reporter.Event(ctx, ev)
}
