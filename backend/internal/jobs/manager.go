package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"amdl/backend/internal/db"
	"amdl/backend/internal/domain"
	"amdl/backend/internal/events"
	"amdl/backend/internal/storage"
)

type Processor interface {
	ValidateRequest(ctx context.Context, req domain.DownloadRequest) (ValidationResult, error)
	ProcessJob(ctx context.Context, job domain.Job, reporter Reporter) error
}

type ValidationResult struct {
	Type       string
	Storefront string
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

func (m *Manager) Submit(ctx context.Context, req domain.DownloadRequest) (domain.Job, error) {
	validated, err := m.processor.ValidateRequest(ctx, req)
	if err != nil {
		return domain.Job{}, err
	}

	// Serialize capacity check, persistence and enqueue. Only Submit writes to
	// the queue, so a free slot cannot disappear while this lock is held.
	m.submitMu.Lock()
	defer m.submitMu.Unlock()
	if len(m.queue) >= cap(m.queue) {
		return domain.Job{}, ErrQueueFull
	}

	now := time.Now().UTC()
	job := domain.Job{
		ID: storage.NewID("job"), Input: req.URL, Type: validated.Type, Storefront: validated.Storefront, Force: req.Force, Status: domain.JobQueued,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := m.store.CreateJob(ctx, job); err != nil {
		return job, err
	}
	_ = m.Event(ctx, domain.Event{JobID: job.ID, Type: "job_queued", Message: "job queued"})
	m.queue <- job.ID
	return job, nil
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
	job.Status = domain.JobCancelled
	job.UpdatedAt = time.Now().UTC()
	if err := m.store.UpdateJob(ctx, job); err != nil {
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
