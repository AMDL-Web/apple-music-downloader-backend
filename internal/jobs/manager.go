package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"amdl/internal/db"
	"amdl/internal/domain"
	"amdl/internal/events"
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
	workers   int
}

func NewManager(store *db.Store, hub *events.Hub, processor Processor, workers int, logger *slog.Logger) *Manager {
	if workers <= 0 {
		workers = 1
	}
	return &Manager{
		store: store, hub: hub, processor: processor, queue: make(chan string, 256),
		logger: logger, cancels: map[string]context.CancelFunc{}, workers: workers,
	}
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
//
// userID attributes accepted jobs to their owner; empty means unowned
// (single-user mode). The canonical key is scoped per user so different users
// can download the same content into their own directories concurrently.
//
// requestOverrides is the request-level config layer. It is merged over the
// submitting user's stored overrides and the result is snapshotted onto every
// accepted job, so later user-config edits or a backend restart never change
// how an already-submitted job downloads. The global config stays the live
// fallback: it is applied underneath the snapshot at execution time.
//
// A non-nil error means the batch was not processed at all (infrastructure
// failure, e.g. the user-config lookup failed) — distinct from per-URL
// rejections, which are reported inside the response.
func (m *Manager) SubmitBatch(ctx context.Context, urls []string, force bool, userID string, requestOverrides *domain.DownloadOverrides) (domain.BatchSubmitResponse, error) {
	results := make([]domain.SubmitResult, len(urls))

	var userOverrides *domain.DownloadOverrides
	if userID != "" {
		user, err := m.store.GetUser(ctx, userID)
		if err != nil {
			return domain.BatchSubmitResponse{}, fmt.Errorf("load user config: %w", err)
		}
		userOverrides = user.Overrides
	}
	overrides := domain.MergeDownloadOverrides(userOverrides, requestOverrides)

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
		if userID != "" {
			key = userID + "|" + key
		}
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
			ID: storage.NewID("job"), UserID: userID, Input: c.url, Type: c.valid.Type, Storefront: c.valid.Storefront, CanonicalKey: c.key,
			Force: force, Overrides: overrides, Status: domain.JobQueued, CreatedAt: now, UpdatedAt: now,
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
	return resp, nil
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
	cancel := m.cancels[jobID]
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	job, err := m.store.GetJob(ctx, jobID)
	if err != nil {
		return err
	}
	if job.Status == domain.JobCompleted || job.Status == domain.JobFailed {
		return nil
	}
	if err := m.store.UpdateJobStatus(ctx, job.ID, domain.JobCancelled, time.Now().UTC()); err != nil {
		return err
	}
	return m.Event(ctx, domain.Event{JobID: jobID, Type: "job_cancelled", Message: "job cancelled"})
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
	job, err := m.store.GetJob(parent, jobID)
	if err != nil {
		m.logger.Error("load job", "job_id", jobID, "error", err)
		return
	}
	ctx, cancel := context.WithCancel(parent)
	m.mu.Lock()
	m.cancels[jobID] = cancel
	m.mu.Unlock()
	defer func() {
		cancel()
		m.mu.Lock()
		delete(m.cancels, jobID)
		m.mu.Unlock()
	}()

	job.Status = domain.JobRunning
	job.UpdatedAt = time.Now().UTC()
	_ = m.store.UpdateJob(ctx, job)
	_ = m.Event(ctx, domain.Event{JobID: job.ID, Type: "job_started", Message: "job started"})

	err = m.processor.ProcessJob(ctx, job, m)
	if latest, loadErr := m.store.GetJob(context.Background(), job.ID); loadErr == nil {
		job = latest
	}
	if err != nil {
		m.refreshCounts(&job)
		if errors.Is(ctx.Err(), context.Canceled) {
			job.Status = domain.JobCancelled
			job.Error = "cancelled"
			_ = m.Event(context.Background(), domain.Event{JobID: job.ID, Type: "job_cancelled", Message: "job cancelled"})
		} else {
			job.Status = domain.JobFailed
			job.Error = err.Error()
			_ = m.Event(context.Background(), domain.Event{JobID: job.ID, Type: "job_failed", Message: err.Error()})
		}
	} else {
		m.refreshCounts(&job)
		if job.FailedItems > 0 {
			job.Status = domain.JobFailed
		} else {
			job.Status = domain.JobCompleted
		}
		_ = m.Event(context.Background(), domain.Event{JobID: job.ID, Type: "job_finished", Message: string(job.Status)})
	}
	job.UpdatedAt = time.Now().UTC()
	_ = m.store.UpdateJob(context.Background(), job)
}

func (m *Manager) refreshCounts(job *domain.Job) {
	items, _ := m.store.ListItems(context.Background(), job.ID)
	failed := 0
	done := 0
	for _, item := range items {
		switch item.Status {
		case domain.ItemFailed:
			failed++
		case domain.ItemCompleted, domain.ItemSkipped:
			done++
		}
	}
	job.DoneItems = done
	job.FailedItems = failed
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
