package jobs

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"amdl/internal/db"
	"amdl/internal/domain"
	"amdl/internal/events"
)

type recoveryProcessor struct{}

func (recoveryProcessor) ValidateRequest(context.Context, domain.DownloadRequest) (ValidationResult, error) {
	return ValidationResult{Type: "song", Storefront: "cn"}, nil
}

func (recoveryProcessor) ProcessJob(context.Context, domain.Job, Reporter) error {
	return nil
}

type cancelAfterTotalProcessor struct {
	started chan struct{}
	once    sync.Once
}

func (p *cancelAfterTotalProcessor) ValidateRequest(context.Context, domain.DownloadRequest) (ValidationResult, error) {
	return ValidationResult{Type: "artist", Storefront: "cn"}, nil
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
	queued := domain.Job{ID: "job-queued", Input: "https://music.apple.com/cn/song/queued/1", Type: "song", Storefront: "cn", Status: domain.JobQueued, CreatedAt: now, UpdatedAt: now}
	running := domain.Job{ID: "job-running", Input: "https://music.apple.com/cn/song/running/2", Type: "song", Storefront: "cn", Status: domain.JobRunning, CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second)}
	completed := domain.Job{ID: "job-completed", Input: "https://music.apple.com/cn/song/completed/3", Type: "song", Storefront: "cn", Status: domain.JobCompleted, CreatedAt: now.Add(2 * time.Second), UpdatedAt: now.Add(2 * time.Second)}
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
	job, err := manager.Submit(ctx, domain.DownloadRequest{URL: "https://music.apple.com/cn/artist/example/1495777901"})
	if err != nil {
		t.Fatal(err)
	}

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
