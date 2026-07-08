package hooks

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"amdl/internal/domain"
)

// EventRecorder persists a domain event (e.g. so hook results show up in the
// job's SSE stream) without internal/hooks importing internal/jobs.
type EventRecorder func(ctx context.Context, ev domain.Event) error

// Dispatcher fans a job-lifecycle event out to every enabled hook entry that
// subscribes to it. Dispatch never blocks the caller: matching hooks run in
// background goroutines bounded by a concurrency semaphore.
type Dispatcher struct {
	cfg      Config
	recorder EventRecorder
	logger   *slog.Logger
	sem      chan struct{}

	// mu guards wg.Add against a concurrent Shutdown. sync.WaitGroup requires
	// that any Add(positive) happening when the counter could be zero
	// happens-before the Wait call that observes it; without this mutex, a
	// worker could call Dispatch (wg.Add) concurrently with Shutdown (wg.Wait)
	// after the dispatcher was supposed to be draining, which is undefined
	// behavior per the sync.WaitGroup docs. closed additionally makes
	// Shutdown a permanent cutover: once called, later Dispatch calls are
	// dropped instead of racing to add more work. mu also guards pending.
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup

	// pending counts, per job id, how many dispatched hook executions haven't
	// recorded their terminal hook_succeeded/hook_failed event yet. A caller
	// deciding whether a job's event stream still has anything left to deliver
	// cannot rely on the job's own terminal event alone: hook executions are
	// fire-and-forget goroutines that can run for as long as their configured
	// timeout, well after the job itself reached a terminal status. See
	// Pending.
	pending map[string]int
}

func NewDispatcher(cfg Config, recorder EventRecorder, logger *slog.Logger) *Dispatcher {
	return &Dispatcher{
		cfg: cfg, recorder: recorder, logger: logger,
		sem:     make(chan struct{}, cfg.Concurrency()),
		pending: map[string]int{},
	}
}

// Dispatch evaluates every entry against event/job and launches the matches
// asynchronously. Safe to call with a no-op Dispatcher (nil-safe) so callers
// don't need to branch when hooks are disabled.
func (d *Dispatcher) Dispatch(event string, job domain.Job, items []domain.JobItem) {
	if d == nil || !d.cfg.Enabled {
		return
	}
	var matched []Entry
	for _, e := range d.cfg.Entries {
		if !e.IsEnabled() || !e.MatchesEvent(event) || !e.MatchesJobType(job.Type) {
			continue
		}
		matched = append(matched, e)
	}
	if len(matched) == 0 {
		return
	}

	payload := Payload{Event: event, Timestamp: time.Now().UTC().Format(time.RFC3339), Job: jobPayload(job), Items: itemPayloads(items)}

	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.wg.Add(len(matched))
	d.pending[job.ID] += len(matched)
	d.mu.Unlock()

	for _, entry := range matched {
		go d.execute(entry, payload)
	}
}

// Pending reports whether jobID has any dispatched hook execution that
// hasn't recorded its terminal hook_succeeded/hook_failed event yet.
// Nil-safe so callers don't need to branch when hooks are disabled.
func (d *Dispatcher) Pending(jobID string) bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pending[jobID] > 0
}

func (d *Dispatcher) execute(entry Entry, payload Payload) {
	defer d.wg.Done()
	defer d.donePending(payload.Job.ID)
	d.sem <- struct{}{}
	defer func() { <-d.sem }()

	runner := runners[entry.Type]
	timeout := entry.Timeout(d.cfg.Timeout())
	ctx := context.Background()

	d.record(ctx, entry, payload.Job.ID, "hook_started", "", nil)

	// max_attempts counts total attempts including the first; 0 (unset)
	// behaves as a single attempt.
	attempts := entry.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		runCtx, cancel := context.WithTimeout(ctx, timeout)
		start := time.Now()
		err := runner.Run(runCtx, entry, payload)
		duration := time.Since(start)
		cancel()
		if err == nil {
			d.record(ctx, entry, payload.Job.ID, "hook_succeeded", "", map[string]any{
				"attempt": attempt, "duration_ms": duration.Milliseconds(),
			})
			return
		}
		lastErr = err
		d.logger.Warn("hook execution failed", "hook", entry.Name, "attempt", attempt, "max_attempts", attempts, "error", err)
		if attempt < attempts {
			time.Sleep(time.Second)
		}
	}
	d.record(ctx, entry, payload.Job.ID, "hook_failed", lastErr.Error(), map[string]any{"attempts": attempts})
}

// donePending runs after this execution's terminal hook_succeeded/hook_failed
// event has already been recorded (record is called synchronously earlier in
// execute, before its deferred calls unwind), so Pending only ever drops to
// false once every dispatched execution's outcome is durably visible.
func (d *Dispatcher) donePending(jobID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pending[jobID]--
	if d.pending[jobID] <= 0 {
		delete(d.pending, jobID)
	}
}

func (d *Dispatcher) record(ctx context.Context, entry Entry, jobID, eventType, message string, extra map[string]any) {
	if d.recorder == nil {
		return
	}
	fields := map[string]any{"hook": entry.Name}
	for k, v := range extra {
		fields[k] = v
	}
	raw, _ := json.Marshal(fields)
	_ = d.recorder(ctx, domain.Event{
		JobID: jobID, Type: eventType, Phase: entry.Name, Message: message, Payload: string(raw),
	})
}

// Shutdown stops accepting new hook work and waits for in-flight executions
// to finish, up to ctx's deadline, so the process doesn't exit mid-webhook on
// SIGTERM. Any Dispatch call after Shutdown has begun is silently dropped
// instead of racing wg.Add against wg.Wait.
func (d *Dispatcher) Shutdown(ctx context.Context) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.closed = true
	d.mu.Unlock()

	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}
