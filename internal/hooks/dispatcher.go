package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"amdl/internal/domain"
)

// EventRecorder persists a domain event (e.g. so hook results show up in the
// job's SSE stream) without internal/hooks importing internal/jobs.
type EventRecorder func(ctx context.Context, ev domain.Event) error

// Runner executes one hook entry against a payload. There are two: the webhook
// and exec runners.
type Runner interface {
	Run(ctx context.Context, entry Entry, payload Payload) error
}

// retryDelay is the wait between two attempts of one hook execution.
const retryDelay = time.Second

// recordTimeout bounds one hook_started/hook_succeeded/hook_failed write. The
// writes run on a context of their own, so a hook cut short by Shutdown still
// records its outcome.
const recordTimeout = 5 * time.Second

// Dispatcher fans a job-lifecycle event out to every enabled hook entry that
// subscribes to it. Dispatch never blocks the caller: matching hooks run in
// background goroutines bounded by a concurrency semaphore.
type Dispatcher struct {
	// cfg is swapped whole by SetConfig when Editor writes hooks.yaml; an
	// execution already dispatched keeps the entry it started with.
	cfg      atomic.Pointer[Config]
	recorder EventRecorder
	logger   *slog.Logger
	sem      chan struct{}

	// runner, when set, runs every entry in place of its type's runner. Tests
	// set it; NewDispatcher leaves it nil.
	runner Runner

	// runCtx is the parent of every hook attempt; abortRuns ends them all.
	// retryCtx, its child, ends retry waits and keeps further attempts from
	// starting. Shutdown cancels retryCtx when it begins and runCtx only when
	// its wait expires, so in-flight attempts drain first and are cut last.
	runCtx      context.Context
	abortRuns   context.CancelFunc
	retryCtx    context.Context
	stopRetries context.CancelFunc

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
	wait   sync.Once
	done   chan struct{}

	// pending counts, per job id, how many dispatched hook executions haven't
	// recorded their terminal hook_succeeded/hook_failed event yet. A caller
	// deciding whether a job's event stream still has anything left to deliver
	// cannot rely on the job's own terminal event alone: hook executions are
	// fire-and-forget goroutines that can run for as long as the hook
	// timeout, well after the job itself reached a terminal status. See
	// Pending.
	pending map[string]int
	// onIdle, set by OnIdle and guarded by mu, runs when a job's pending
	// count drops to zero.
	onIdle func(jobID string)
}

func NewDispatcher(cfg Config, recorder EventRecorder, logger *slog.Logger) *Dispatcher {
	runCtx, abortRuns := context.WithCancel(context.Background())
	retryCtx, stopRetries := context.WithCancel(runCtx)
	d := &Dispatcher{
		recorder: recorder, logger: logger,
		sem:    make(chan struct{}, maxConcurrent),
		runCtx: runCtx, abortRuns: abortRuns, retryCtx: retryCtx, stopRetries: stopRetries,
		pending: map[string]int{},
		done:    make(chan struct{}),
	}
	d.cfg.Store(&cfg)
	return d
}

// SetConfig replaces the configuration later events are dispatched with.
func (d *Dispatcher) SetConfig(cfg Config) {
	d.cfg.Store(&cfg)
}

// Listing is the public, sanitized view of the hooks configuration. It
// deliberately omits secret-bearing fields such as URLs, headers, commands,
// and working directories.
type Listing struct {
	Enabled bool          `json:"enabled"`
	Entries []ListedEntry `json:"entries"`
}

// ListedEntry is one entry of Listing. A webhook's address shows only as
// its origin (scheme and host), its headers only by name: the path, query,
// header values and body may hold tokens.
type ListedEntry struct {
	Name        string   `json:"name"`
	Enabled     bool     `json:"enabled"`
	Type        string   `json:"type"`
	Events      []string `json:"events"`
	JobTypes    []string `json:"job_types"`
	Preset      string   `json:"preset,omitempty"`
	Method      string   `json:"method,omitempty"`
	Origin      string   `json:"origin,omitempty"`
	HeaderNames []string `json:"header_names,omitempty"`
	MaxAttempts int      `json:"max_attempts,omitempty"`
}

// List returns configured entries in file order. Nil-safe for deployments
// with no dispatcher configured.
func (d *Dispatcher) List() Listing {
	if d == nil {
		return listingOf(Default())
	}
	return listingOf(*d.cfg.Load())
}

func listingOf(cfg Config) Listing {
	listing := Listing{Enabled: cfg.Enabled, Entries: make([]ListedEntry, 0, len(cfg.Entries))}
	for _, entry := range cfg.Entries {
		listing.Entries = append(listing.Entries, listedEntry(entry))
	}
	return listing
}

func listedEntry(entry Entry) ListedEntry {
	out := ListedEntry{
		Name:     entry.Name,
		Enabled:  entry.IsEnabled(),
		Type:     entry.Type,
		Events:   append([]string{}, entry.Events...),
		JobTypes: append([]string{}, entry.JobTypes...),
	}
	if entry.Type != typeWebhook {
		return out
	}
	out.Preset, out.Method, out.MaxAttempts = entry.Preset, entry.Method, entry.MaxAttempts
	if out.Method == "" {
		out.Method = http.MethodPost
	}
	if out.MaxAttempts < 1 {
		out.MaxAttempts = 1
	}
	if u, err := url.Parse(entry.URL); err == nil && u.Host != "" {
		out.Origin = u.Scheme + "://" + u.Host
	}
	for name := range entry.Headers {
		out.HeaderNames = append(out.HeaderNames, name)
	}
	sort.Strings(out.HeaderNames)
	return out
}

// Dispatch evaluates every entry against the event and the job's type, then
// launches the enabled matches asynchronously. Safe to call with
// a no-op Dispatcher (nil-safe) so callers don't need to branch when hooks are
// disabled.
func (d *Dispatcher) Dispatch(event string, job domain.Job, items []domain.JobItem) {
	if d == nil {
		return
	}
	cfg := d.cfg.Load()
	if !cfg.Enabled {
		return
	}
	var matched []Entry
	for _, e := range cfg.Entries {
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
		// Only a worker that outlived its shutdown step gets here; the store
		// may already be closed, so the log is the only record.
		d.logger.Warn("hooks not run: the dispatcher has shut down", "job_id", job.ID, "hook_event", event, "hooks", len(matched))
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
	logger := d.logger.With("job_id", payload.Job.ID, "hook", entry.Name, "hook_event", payload.Event)
	select {
	case d.sem <- struct{}{}:
	case <-d.runCtx.Done():
		d.record(logger, entry, payload.Job.ID, domain.EventHookFailed, "not run: the backend shut down first", map[string]any{"attempts": 0})
		return
	}
	defer func() { <-d.sem }()

	runner := d.runnerFor(entry)

	d.record(logger, entry, payload.Job.ID, domain.EventHookStarted, "", nil)
	logger.Debug("hook execution started")

	// max_attempts counts total attempts including the first; 0 (unset)
	// behaves as a single attempt.
	maxAttempts := entry.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	var lastErr error
	attempt := 0
	for attempt < maxAttempts {
		attempt++
		runCtx, cancel := context.WithTimeout(d.runCtx, hookTimeout)
		start := time.Now()
		err := runner.Run(runCtx, entry, payload)
		duration := time.Since(start)
		cancel()
		if err == nil {
			d.record(logger, entry, payload.Job.ID, domain.EventHookSucceeded, "", map[string]any{
				"attempt": attempt, "duration_ms": duration.Milliseconds(),
			})
			logger.Info("hook execution succeeded", "attempt", attempt, "duration_ms", duration.Milliseconds())
			return
		}
		lastErr = err
		logger.Warn("hook execution failed", "attempt", attempt, "max_attempts", maxAttempts, "duration_ms", duration.Milliseconds(), "error", err)
		if attempt < maxAttempts && !d.waitRetry() {
			lastErr = fmt.Errorf("%w (not retried: the backend is shutting down)", lastErr)
			break
		}
	}
	d.record(logger, entry, payload.Job.ID, domain.EventHookFailed, lastErr.Error(), map[string]any{"attempts": attempt})
}

// runnerFor picks the runner for an entry's type. validate has already
// rejected any other type.
func (d *Dispatcher) runnerFor(entry Entry) Runner {
	if d.runner != nil {
		return d.runner
	}
	if entry.Type == typeWebhook {
		return &webhookRunner{client: webhookClient}
	}
	return &execRunner{}
}

// waitRetry waits retryDelay before another attempt and reports whether to
// make it: false as soon as Shutdown has begun.
func (d *Dispatcher) waitRetry() bool {
	timer := time.NewTimer(retryDelay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return d.retryCtx.Err() == nil
	case <-d.retryCtx.Done():
		return false
	}
}

// donePending runs after this execution's terminal hook_succeeded/hook_failed
// event has already been recorded (record is called synchronously earlier in
// execute, before its deferred calls unwind), so Pending only ever drops to
// false once every dispatched execution's outcome is durably visible.
//
// When the last pending execution of a job ends, the idle callback runs, so a
// stream that saw the last hook event while the hook still counted as pending
// can end now instead of at its next re-drain.
func (d *Dispatcher) donePending(jobID string) {
	d.mu.Lock()
	d.pending[jobID]--
	idle := d.pending[jobID] <= 0
	if idle {
		delete(d.pending, jobID)
	}
	onIdle := d.onIdle
	d.mu.Unlock()
	if idle && onIdle != nil {
		onIdle(jobID)
	}
}

// OnIdle sets the function called with a job's id when its last pending hook
// execution ends.
func (d *Dispatcher) OnIdle(f func(jobID string)) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.onIdle = f
	d.mu.Unlock()
}

// record persists one hook lifecycle event through the recorder. It runs on
// its own bounded context, detached from the attempt that may just have been
// cut short. A failure is logged: the outcome then shows only in the logs.
func (d *Dispatcher) record(logger *slog.Logger, entry Entry, jobID string, eventType domain.EventType, message string, extra map[string]any) {
	if d.recorder == nil {
		return
	}
	fields := map[string]any{"hook": entry.Name}
	for k, v := range extra {
		fields[k] = v
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		logger.Error("encode hook event payload", "event_type", string(eventType), "error", err)
		raw = nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), recordTimeout)
	defer cancel()
	if err := d.recorder(ctx, domain.Event{
		JobID: jobID, Type: eventType, Phase: entry.Name, Message: message, Payload: raw,
	}); err != nil {
		logger.Error("record hook event", "event_type", string(eventType), "error", err)
	}
}

// Shutdown stops accepting new hook work, stops retries, and waits for
// in-flight attempts to finish, up to ctx's deadline, so the process doesn't
// exit mid-webhook on SIGTERM. When the deadline expires it cancels every
// remaining attempt (each then records hook_failed) and returns ctx's error;
// call Wait to see them exit. Any Dispatch call after Shutdown has begun is
// silently dropped instead of racing wg.Add against wg.Wait.
func (d *Dispatcher) Shutdown(ctx context.Context) error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	d.closed = true
	d.mu.Unlock()
	d.stopRetries()
	if err := d.Wait(ctx); err != nil {
		d.abortRuns()
		return err
	}
	return nil
}

// Wait waits for hook executions after Shutdown has stopped new dispatches.
// It can be called again with a longer-lived context after a bounded shutdown
// wait expires, ensuring dependencies are not closed under active hooks.
func (d *Dispatcher) Wait(ctx context.Context) error {
	if d == nil {
		return nil
	}
	d.wait.Do(func() {
		go func() {
			d.wg.Wait()
			close(d.done)
		}()
	})
	select {
	case <-d.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
