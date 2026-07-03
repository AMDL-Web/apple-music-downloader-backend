package jobs

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"amdl/internal/db"
	"amdl/internal/domain"
	"amdl/internal/events"
)

type recoveryProcessor struct{}

func (recoveryProcessor) ValidateRequest(context.Context, string) (ValidationResult, error) {
	return ValidationResult{Type: "song", Storefront: "cn"}, nil
}

func (recoveryProcessor) ProcessJob(context.Context, domain.Job, Reporter) error {
	return nil
}

type cancelAfterTotalProcessor struct {
	started chan struct{}
	once    sync.Once
}

func (p *cancelAfterTotalProcessor) ValidateRequest(context.Context, string) (ValidationResult, error) {
	return ValidationResult{Type: "artist", Storefront: "cn", ID: "1495777901"}, nil
}

func (p *cancelAfterTotalProcessor) ProcessJob(ctx context.Context, job domain.Job, reporter Reporter) error {
	job.TotalItems = 2
	if err := reporter.SetJob(ctx, job); err != nil {
		return err
	}
	for i := 1; i <= 2; i++ {
		if err := reporter.AddItem(ctx, domain.JobItem{
			JobID: job.ID, AdamID: "song", Kind: "song", Index: i, Title: "Song", Status: domain.ItemQueued,
		}); err != nil {
			return err
		}
	}
	p.once.Do(func() { close(p.started) })
	<-ctx.Done()
	return ctx.Err()
}

func TestRecoverUnfinishedRequeuesQueuedAndRunningJobs(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	queued := domain.Job{ID: "job-queued", Input: "https://music.apple.com/cn/song/queued/1", Type: "song", Storefront: "cn", CanonicalKey: "song:cn:1", Status: domain.JobQueued, CreatedAt: now, UpdatedAt: now}
	running := domain.Job{ID: "job-running", Input: "https://music.apple.com/cn/song/running/2", Type: "song", Storefront: "cn", CanonicalKey: "song:cn:2", Status: domain.JobRunning, CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second)}
	completed := domain.Job{ID: "job-completed", Input: "https://music.apple.com/cn/song/completed/3", Type: "song", Storefront: "cn", CanonicalKey: "song:cn:3", Status: domain.JobCompleted, CreatedAt: now.Add(2 * time.Second), UpdatedAt: now.Add(2 * time.Second)}
	for _, job := range []domain.Job{queued, running, completed} {
		if err := store.CreateJob(ctx, job); err != nil {
			t.Fatal(err)
		}
	}

	manager := NewManager(store, events.NewHub(), recoveryProcessor{}, 1, slog.Default())
	recovered, err := manager.RecoverUnfinished(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if recovered != 2 {
		t.Fatalf("recovered = %d, want 2", recovered)
	}
	gotRunning, err := store.GetJob(ctx, running.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotRunning.Status != domain.JobQueued {
		t.Fatalf("running job status = %s, want %s", gotRunning.Status, domain.JobQueued)
	}
	gotCompleted, err := store.GetJob(ctx, completed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotCompleted.Status != domain.JobCompleted {
		t.Fatalf("completed job status = %s, want %s", gotCompleted.Status, domain.JobCompleted)
	}
	if len(manager.queue) != 2 {
		t.Fatalf("queue length = %d, want 2", len(manager.queue))
	}
	if first, second := <-manager.queue, <-manager.queue; first != queued.ID || second != running.ID {
		t.Fatalf("queued ids = [%s, %s], want [%s, %s]", first, second, queued.ID, running.ID)
	}
	events, err := store.ListEventsAfter(ctx, running.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "job_recovered" {
		t.Fatalf("running job recovery events = %+v, want one job_recovered", events)
	}
}

func TestCancelledJobPreservesProcessorUpdatedTotalItems(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	processor := &cancelAfterTotalProcessor{started: make(chan struct{})}
	manager := NewManager(store, events.NewHub(), processor, 1, slog.Default())
	manager.Start(ctx)
	resp := manager.SubmitBatch(ctx, []string{"https://music.apple.com/cn/artist/example/1495777901"}, false, "")
	if resp.Accepted != 1 || len(resp.Results) != 1 || resp.Results[0].Status != domain.SubmitAccepted || resp.Results[0].Job == nil {
		t.Fatalf("unexpected submit result: %+v", resp)
	}
	job := *resp.Results[0].Job

	select {
	case <-processor.started:
	case <-time.After(2 * time.Second):
		t.Fatal("processor did not start")
	}
	if err := manager.Cancel(ctx, job.ID); err != nil {
		t.Fatal(err)
	}

	var got domain.Job
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		got, err = store.GetJob(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == domain.JobCancelled && got.Error == "cancelled" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got.Status != domain.JobCancelled {
		t.Fatalf("status = %s, want %s", got.Status, domain.JobCancelled)
	}
	if got.TotalItems != 2 {
		t.Fatalf("total_items = %d, want 2", got.TotalItems)
	}
}

// keyedProcessor derives ValidationResult from a "type|storefront|id" test
// URL so canonical-key dedup can be exercised without real Apple Music
// parsing. URLs prefixed "bad:" are treated as invalid.
type keyedProcessor struct{}

func (keyedProcessor) ValidateRequest(_ context.Context, url string) (ValidationResult, error) {
	if strings.HasPrefix(url, "bad:") {
		return ValidationResult{}, &RequestError{Code: "invalid_url", Message: "bad test url"}
	}
	parts := strings.SplitN(url, "|", 3)
	if len(parts) != 3 {
		return ValidationResult{}, &RequestError{Code: "invalid_url", Message: "malformed test url"}
	}
	return ValidationResult{Type: parts[0], Storefront: parts[1], ID: parts[2]}, nil
}

func (keyedProcessor) ProcessJob(context.Context, domain.Job, Reporter) error {
	return nil
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return NewManager(store, events.NewHub(), keyedProcessor{}, 1, slog.Default())
}

func TestSubmitBatchDedupesWithinRequest(t *testing.T) {
	manager := newTestManager(t)
	ctx := context.Background()
	resp := manager.SubmitBatch(ctx, []string{
		"album|us|111",
		"song|us|222",
		"album|us|111", // same canonical key as the first entry
	}, false, "")
	if len(resp.Results) != 3 {
		t.Fatalf("results = %+v, want 3", resp.Results)
	}
	if resp.Results[0].Status != domain.SubmitAccepted || resp.Results[1].Status != domain.SubmitAccepted {
		t.Fatalf("first two results = %+v, want accepted", resp.Results[:2])
	}
	if resp.Results[2].Status != domain.SubmitDuplicateInRequest {
		t.Fatalf("third result = %+v, want duplicate_in_request", resp.Results[2])
	}
	if resp.Accepted != 2 || resp.Rejected != 1 {
		t.Fatalf("resp = %+v, want 2 accepted / 1 rejected", resp)
	}
}

func TestSubmitBatchRejectsActiveDuplicateButAllowsAfterCompletion(t *testing.T) {
	manager := newTestManager(t)
	ctx := context.Background()

	first := manager.SubmitBatch(ctx, []string{"song|us|222"}, false, "")
	if first.Accepted != 1 {
		t.Fatalf("first submit = %+v, want 1 accepted", first)
	}
	jobID := first.Results[0].Job.ID

	second := manager.SubmitBatch(ctx, []string{"song|us|222"}, false, "")
	if second.Results[0].Status != domain.SubmitDuplicateActive || second.Results[0].ExistingJobID != jobID {
		t.Fatalf("second submit = %+v, want duplicate_active for %s", second.Results[0], jobID)
	}

	if err := manager.store.UpdateJobStatus(ctx, jobID, domain.JobCompleted, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	third := manager.SubmitBatch(ctx, []string{"song|us|222"}, false, "")
	if third.Results[0].Status != domain.SubmitAccepted {
		t.Fatalf("third submit = %+v, want accepted after completion", third.Results[0])
	}
}

func TestSubmitBatchQueueFullMarksRemainingWithoutRollback(t *testing.T) {
	manager := newTestManager(t)
	manager.queue = make(chan string, 1)
	ctx := context.Background()

	resp := manager.SubmitBatch(ctx, []string{"song|us|1", "song|us|2", "song|us|3"}, false, "")
	if resp.Results[0].Status != domain.SubmitAccepted {
		t.Fatalf("first = %+v, want accepted", resp.Results[0])
	}
	if resp.Results[1].Status != domain.SubmitQueueFull || resp.Results[2].Status != domain.SubmitQueueFull {
		t.Fatalf("remaining = %+v, want queue_full", resp.Results[1:])
	}
	if resp.Accepted != 1 || resp.Rejected != 2 {
		t.Fatalf("resp = %+v, want 1 accepted / 2 rejected", resp)
	}
}

func TestSubmitBatchInvalidURLReportsError(t *testing.T) {
	manager := newTestManager(t)
	resp := manager.SubmitBatch(context.Background(), []string{"bad:not-a-url"}, false, "")
	if resp.Results[0].Status != domain.SubmitInvalid || resp.Results[0].Error == "" {
		t.Fatalf("result = %+v, want invalid with error message", resp.Results[0])
	}
}
