package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"amdl/internal/db"
	"amdl/internal/domain"
	"amdl/internal/events"
	"amdl/internal/hooks"
	"amdl/internal/storage"
)

type Processor interface {
	ValidateRequest(ctx context.Context, url string) (ValidationResult, error)
	ProcessJob(ctx context.Context, job domain.Job, reporter Reporter) error
}

type ValidationResult struct {
	Type       string
	Storefront string
	ID         string
}

type RequestError struct {
	Code                 string
	Message              string
	Storefront           string
	SupportedStorefronts []string
	Cause                error
}

func (e *RequestError) Error() string { return e.Message }

func (e *RequestError) Unwrap() error { return e.Cause }

var ErrQueueFull = errors.New("job queue is full")

type Reporter interface {
	SetJob(ctx context.Context, job domain.Job) error
	AddItem(ctx context.Context, item domain.JobItem) error
	UpdateItem(ctx context.Context, item domain.JobItem) error
	Event(ctx context.Context, ev domain.Event) error
}

type Manager struct {
	store     *db.Store
	hub       *events.Hub
	processor Processor
	queue     chan string
	logger    *slog.Logger
	mu        sync.Mutex
	submitMu  sync.Mutex
	cancels   map[string]context.CancelFunc
	// finalizing marks jobs whose terminal status is already persisted but
	// whose finalize sequence (terminal event + hook dispatch) has not finished
	// yet via Cancel's queued path. run()'s finalize window needs no entry here
	// because m.cancels holds the job until finalizeLogged returns. Delete
	// refuses jobs present in either map.
	finalizing map[string]bool
	workers    int
	hooks      *hooks.Dispatcher
}

func NewManager(store *db.Store, hub *events.Hub, processor Processor, workers int, logger *slog.Logger) *Manager {
	if workers <= 0 {
		workers = 1
	}
	return &Manager{
		store: store, hub: hub, processor: processor, queue: make(chan string, 256),
		logger: logger, cancels: map[string]context.CancelFunc{}, finalizing: map[string]bool{}, workers: workers,
	}
}

// SetHooks wires the post-download hook dispatcher. Called after
// construction because the dispatcher's event recorder is Manager.Event
// itself. A nil dispatcher (the zero value, since hooks is unset until this
// is called) is a safe no-op — Dispatcher.Dispatch handles a nil receiver.
func (m *Manager) SetHooks(d *hooks.Dispatcher) {
	m.hooks = d
}

// HooksPending reports whether jobID has a post-download hook still running.
// A job's own terminal event is not the last event it will ever emit: hook
// dispatch is fire-and-forget and can keep recording hook_started/
// hook_succeeded/hook_failed events well after the job itself reached a
// terminal status, so callers deciding whether an event stream has anything
// left to deliver must check this too. Nil-safe (nil *Manager, unset hooks).
func (m *Manager) HooksPending(jobID string) bool {
	if m == nil {
		return false
	}
	return m.hooks.Pending(jobID)
}

func (m *Manager) Start(ctx context.Context) {
	for i := 0; i < m.workers; i++ {
		go m.worker(ctx, i)
	}
}

func (m *Manager) RecoverUnfinished(ctx context.Context) (int, error) {
	jobs, err := m.store.ListRecoverableJobs(ctx)
	if err != nil {
		return 0, err
	}
	m.submitMu.Lock()
	defer m.submitMu.Unlock()
	if len(jobs) > cap(m.queue)-len(m.queue) {
		return 0, ErrQueueFull
	}
	now := time.Now().UTC()
	for _, job := range jobs {
		if job.Status == domain.JobRunning {
			job.Status = domain.JobQueued
			job.Error = ""
			job.UpdatedAt = now
			if err := m.store.UpdateJob(ctx, job); err != nil {
				return 0, err
			}
		}
		if err := m.Event(ctx, domain.Event{JobID: job.ID, Type: "job_recovered", Message: "job recovered after backend restart"}); err != nil {
			return 0, err
		}
		m.queue <- job.ID
	}
	return len(jobs), nil
}

// SubmitBatch validates and enqueues each url independently, applying
// three-layer dedup: request-internal (canonical key seen earlier in this
// batch), active-job lookup, and the DB's partial unique index as a backstop
// against races. See docs/multi-link-submit-design.md.
func (m *Manager) SubmitBatch(ctx context.Context, urls []string, force bool) domain.BatchSubmitResponse {
	results := make([]domain.SubmitResult, len(urls))

	type candidate struct {
		index int
		url   string
		key   string
		valid ValidationResult
	}
	var candidates []candidate
	seenKeys := map[string]bool{}
	for i, url := range urls {
		validated, err := m.processor.ValidateRequest(ctx, url)
		if err != nil {
			results[i] = domain.SubmitResult{URL: url, Status: domain.SubmitInvalid, Error: requestErrorMessage(err)}
			continue
		}
		key := validated.Type + ":" + validated.Storefront + ":" + validated.ID
		if seenKeys[key] {
			results[i] = domain.SubmitResult{URL: url, Status: domain.SubmitDuplicateInRequest}
			continue
		}
		seenKeys[key] = true
		candidates = append(candidates, candidate{index: i, url: url, key: key, valid: validated})
	}

	// Serialize capacity check, dedup lookup, persistence and enqueue. Only
	// SubmitBatch writes to the queue, so a free slot cannot disappear while
	// this lock is held.
	m.submitMu.Lock()
	defer m.submitMu.Unlock()

	now := time.Now().UTC()
	queueFull := false
	for _, c := range candidates {
		if queueFull {
			results[c.index] = domain.SubmitResult{URL: c.url, Status: domain.SubmitQueueFull}
			continue
		}
		existing, found, err := m.store.FindActiveJobByKey(ctx, c.key)
		if err != nil {
			results[c.index] = domain.SubmitResult{URL: c.url, Status: domain.SubmitInvalid, Error: err.Error()}
			continue
		}
		if found {
			results[c.index] = domain.SubmitResult{URL: c.url, Status: domain.SubmitDuplicateActive, ExistingJobID: existing.ID}
			continue
		}
		if len(m.queue) >= cap(m.queue) {
			queueFull = true
			results[c.index] = domain.SubmitResult{URL: c.url, Status: domain.SubmitQueueFull}
			continue
		}

		job := domain.Job{
			ID: storage.NewID("job"), Input: c.url, Type: c.valid.Type, Storefront: c.valid.Storefront, CanonicalKey: c.key,
			Force: force, Status: domain.JobQueued, CreatedAt: now, UpdatedAt: now,
		}
		if err := m.store.CreateJob(ctx, job); err != nil {
			if errors.Is(err, db.ErrDuplicateActive) {
				results[c.index] = domain.SubmitResult{URL: c.url, Status: domain.SubmitDuplicateActive}
				continue
			}
			results[c.index] = domain.SubmitResult{URL: c.url, Status: domain.SubmitInvalid, Error: err.Error()}
			continue
		}
		_ = m.Event(ctx, domain.Event{JobID: job.ID, Type: "job_queued", Message: "job queued"})
		m.queue <- job.ID
		accepted := job
		results[c.index] = domain.SubmitResult{URL: c.url, Status: domain.SubmitAccepted, Job: &accepted}
	}

	resp := domain.BatchSubmitResponse{Results: results}
	for _, r := range results {
		if r.Status == domain.SubmitAccepted {
			resp.Accepted++
		} else {
			resp.Rejected++
		}
	}
	return resp
}

func requestErrorMessage(err error) string {
	var reqErr *RequestError
	if errors.As(err, &reqErr) {
		return reqErr.Code + ": " + reqErr.Message
	}
	return err.Error()
}

func (m *Manager) Cancel(ctx context.Context, jobID string) error {
	m.mu.Lock()
	if cancel := m.cancels[jobID]; cancel != nil {
		m.mu.Unlock()
		// The job is actively running under a worker. Cancelling its context
		// is enough: run() observes ctx.Err() once ProcessJob returns and
		// finalizes the job (status, event, hook dispatch) exactly once.
		// Writing the terminal state here too would race with that write
		// and double-fire the job_cancelled hook.
		cancel()
		return nil
	}

	// The job is not running under a worker. Read and write its terminal
	// status while still holding m.mu: run()'s startup claim takes the same
	// lock and performs its status-check + mark-running under it, so this
	// critical section is mutually exclusive with a worker claiming the job.
	// Without this, a worker could observe "queued" and mark the job running
	// in the window between our map read and status write, resurrecting a
	// job we just cancelled.
	job, err := m.store.GetJob(ctx, jobID)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if job.Status.IsTerminal() {
		m.mu.Unlock()
		return nil
	}
	job.Status = domain.JobCancelled
	job.Error = "cancelled"
	job.UpdatedAt = time.Now().UTC()
	persistErr := m.persistTerminal(ctx, job, "job_cancelled", "job cancelled")
	if persistErr == nil {
		// Mark the finalize sequence in flight before releasing m.mu: the
		// status row (and its terminal event, written atomically alongside
		// it) now say cancelled, but hook dispatch below still reads the
		// job's rows. Delete must not remove them in that window.
		m.finalizing[jobID] = true
	}
	m.mu.Unlock()

	// Only report success and dispatch hooks once the terminal status is
	// durably persisted.
	if persistErr != nil {
		return persistErr
	}
	m.dispatchHooks(ctx, job)
	m.mu.Lock()
	delete(m.finalizing, jobID)
	m.mu.Unlock()
	return nil
}

// Delete removes a terminal job and its items/events from the store. It
// refuses while the job is running or while a finalize sequence is still in
// flight: the jobs row already says completed/failed/cancelled before the
// terminal event is inserted and the hook dispatcher reads the items, so
// deleting on the row status alone would orphan the late event insert and
// hand hooks an empty item list. The check and the delete both happen under
// m.mu, which run()'s claim and Cancel's queued path also hold, so a job
// cannot start (or finish) finalizing between the check and the delete.
func (m *Manager) Delete(ctx context.Context, jobID string) error {
	m.mu.Lock()
	if m.cancels[jobID] != nil || m.finalizing[jobID] {
		m.mu.Unlock()
		return db.ErrJobNotTerminal
	}
	deleted, err := m.store.DeleteJob(ctx, jobID)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	// Broadcast the persisted tombstone so live overview subscribers can drop the
	// job immediately; missed broadcasts are replayed from job_events by cursor.
	m.hub.Publish(deleted)
	return nil
}

func (m *Manager) worker(ctx context.Context, index int) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-m.queue:
			m.run(ctx, id)
		}
	}
}

func (m *Manager) run(parent context.Context, jobID string) {
	ctx, cancel := context.WithCancel(parent)

	// Claim the job atomically under m.mu: read its current status, and only
	// if it is still startable, mark it running and register the cancel func.
	// Cancel() takes the same lock for its queued-cancel transition, so the
	// two cannot interleave — either we mark running first (and a concurrent
	// Cancel then finds the cancel func and cancels our context) or Cancel
	// marks cancelled first (and we observe the terminal status here and bail).
	m.mu.Lock()
	job, err := m.store.GetJob(parent, jobID)
	if err != nil {
		m.mu.Unlock()
		cancel()
		m.logger.Error("load job", "job_id", jobID, "error", err)
		return
	}
	if job.Status.IsTerminal() {
		// Cancel() (or another path) already finalized this job while it was
		// queued. Do not resurrect it; its terminal hook was already dispatched.
		m.mu.Unlock()
		cancel()
		return
	}
	job.Status = domain.JobRunning
	job.UpdatedAt = time.Now().UTC()
	if err := m.store.UpdateJob(ctx, job); err != nil {
		m.mu.Unlock()
		cancel()
		m.logger.Error("mark job running", "job_id", jobID, "error", err)
		return
	}
	m.cancels[jobID] = cancel
	m.mu.Unlock()

	defer func() {
		cancel()
		m.mu.Lock()
		delete(m.cancels, jobID)
		m.mu.Unlock()
	}()

	_ = m.Event(ctx, domain.Event{JobID: job.ID, Type: "job_started", Message: "job started"})

	err = m.processor.ProcessJob(ctx, job, m)
	if latest, loadErr := m.store.GetJob(context.Background(), job.ID); loadErr == nil {
		job = latest
	}
	m.refreshCounts(&job)

	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		// Cancellation takes priority over ProcessJob's return value: a job
		// cancelled mid-flight must never be reported as completed, even if
		// ProcessJob happened to return nil (e.g. it finished its last item
		// just as the cancel arrived, or it doesn't surface ctx errors).
		job.Status = domain.JobCancelled
		job.Error = "cancelled"
		m.finalizeLogged(job, "job_cancelled", "job cancelled")
	case err != nil:
		job.Status = domain.JobFailed
		job.Error = err.Error()
		m.finalizeLogged(job, "job_failed", err.Error())
	default:
		if job.FailedItems > 0 {
			job.Status = domain.JobFailed
		} else {
			job.Status = domain.JobCompleted
		}
		m.finalizeLogged(job, "job_finished", string(job.Status))
	}
}

// finalizeJob persists a job's terminal status, emits the corresponding
// domain event, and dispatches any post-download hooks subscribed to that
// status. Shared by run() and Cancel() so every path that reaches a terminal
// status goes through hook dispatch exactly once.
//
// The hook is dispatched only after the terminal status is durably persisted:
// an external system must not be told a job completed/cancelled when the
// backend could not even record that fact. A persistence failure is returned
// so callers can propagate or log it, and no hook fires.
func (m *Manager) finalizeJob(ctx context.Context, job domain.Job, eventType, message string) error {
	job.UpdatedAt = time.Now().UTC()
	if err := m.persistTerminal(ctx, job, eventType, message); err != nil {
		return err
	}
	m.dispatchHooks(ctx, job)
	return nil
}

// persistTerminal atomically persists job's terminal status together with its
// terminal domain event (Store.FinalizeJob), then publishes the stored event
// to the hub. The atomicity matters to callers of GET .../events(/ws): they
// read a job's status and its events table independently, so if these two
// writes ever landed as separate statements, such a reader could observe the
// terminal status with the terminal event still missing and wrongly conclude
// no more events are pending.
func (m *Manager) persistTerminal(ctx context.Context, job domain.Job, eventType, message string) error {
	ev := domain.Event{JobID: job.ID, Type: eventType, Message: message}
	if ev.Payload == "" && ev.Message != "" {
		raw, _ := json.Marshal(map[string]string{"message": ev.Message})
		ev.Payload = string(raw)
	}
	stored, err := m.store.FinalizeJob(ctx, job, ev)
	if err != nil {
		return err
	}
	m.hub.Publish(stored)
	return nil
}

// dispatchHooks fires any post-download hooks subscribed to job's terminal
// status. Must be called only after persistTerminal has durably committed the
// terminal status and its event, and never while holding m.mu (Dispatch is
// non-blocking, but the store read here should not extend the lock).
func (m *Manager) dispatchHooks(ctx context.Context, job domain.Job) {
	if hookEvent := hookEventForStatus(job.Status); hookEvent != "" {
		items, _ := m.store.ListItems(ctx, job.ID)
		m.hooks.Dispatch(hookEvent, job, items)
	}
}

// finalizeLogged is the worker-path wrapper around finalizeJob: there is no
// caller to return an error to, so a persistence failure is logged (and the
// hook is correctly skipped by finalizeJob).
func (m *Manager) finalizeLogged(job domain.Job, eventType, message string) {
	if err := m.finalizeJob(context.Background(), job, eventType, message); err != nil {
		m.logger.Error("finalize job", "job_id", job.ID, "status", string(job.Status), "error", err)
	}
}

// hookEventForStatus maps a job's final status to the hook event name hook
// entries subscribe to in hooks.yaml. Returns "" for non-terminal statuses.
func hookEventForStatus(status domain.JobStatus) string {
	switch status {
	case domain.JobCompleted:
		return "job_finished"
	case domain.JobFailed:
		return "job_failed"
	case domain.JobCancelled:
		return "job_cancelled"
	default:
		return ""
	}
}

func (m *Manager) refreshCounts(job *domain.Job) {
	items, _ := m.store.ListItems(context.Background(), job.ID)
	job.DoneItems, job.FailedItems = domain.CountItemProgress(items)
}

func (m *Manager) SetJob(ctx context.Context, job domain.Job) error {
	job.UpdatedAt = time.Now().UTC()
	return m.store.UpdateJob(ctx, job)
}

func (m *Manager) AddItem(ctx context.Context, item domain.JobItem) error {
	now := time.Now().UTC()
	item.CreatedAt = now
	item.UpdatedAt = now
	if item.ID == "" {
		item.ID = storage.NewID("item")
	}
	return m.store.CreateItem(ctx, item)
}

func (m *Manager) UpdateItem(ctx context.Context, item domain.JobItem) error {
	item.UpdatedAt = time.Now().UTC()
	return m.store.UpdateItem(ctx, item)
}

func (m *Manager) Event(ctx context.Context, ev domain.Event) error {
	if ev.Payload == "" && ev.Message != "" {
		raw, _ := json.Marshal(map[string]string{"message": ev.Message})
		ev.Payload = string(raw)
	}
	stored, err := m.store.AddEvent(ctx, ev)
	if err != nil {
		return err
	}
	m.hub.Publish(stored)
	return nil
}
