package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"amdl/internal/domain"
)

func TestJobItemRetryStateRoundTrip(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	want := domain.JobItem{
		ID: "item-1", JobID: "job-1", AdamID: "123", Kind: "song", Index: 1,
		Status: domain.ItemDownloading, RetryKind: "codec", Attempt: 2, MaxAttempts: 4,
		StatusMessage: "ALAC 第 2/4 次尝试", CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateItem(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListItems(context.Background(), want.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	got := items[0]
	if got.RetryKind != want.RetryKind || got.Attempt != want.Attempt || got.MaxAttempts != want.MaxAttempts || got.StatusMessage != want.StatusMessage {
		t.Fatalf("retry state = %+v, want %+v", got, want)
	}
}

func TestJobForceRoundTrip(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	want := domain.Job{
		ID: "job-force", Input: "https://music.apple.com/cn/song/test/1", Type: "song", Storefront: "cn",
		Force: true, Status: domain.JobQueued, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateJob(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetJob(context.Background(), want.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Force {
		t.Fatal("Force was not persisted")
	}
}

func TestUpdateJobStatusPreservesCountsAndError(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	job := domain.Job{
		ID: "job-status", Input: "https://music.apple.com/cn/artist/example/1", Type: "artist", Storefront: "cn",
		Status: domain.JobRunning, TotalItems: 4, DoneItems: 2, FailedItems: 1, Error: "partial", CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	if err := store.UpdateJobStatus(ctx, job.ID, domain.JobCancelled, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.JobCancelled {
		t.Fatalf("status = %s, want %s", got.Status, domain.JobCancelled)
	}
	if got.TotalItems != 4 || got.DoneItems != 2 || got.FailedItems != 1 || got.Error != "partial" {
		t.Fatalf("job fields were overwritten: %+v", got)
	}
}

func TestListRecoverableJobsOnlyReturnsQueuedAndRunning(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	base := time.Now().UTC()
	jobs := []domain.Job{
		{ID: "completed", Input: "https://music.apple.com/cn/song/completed/1", Type: "song", Storefront: "cn", CanonicalKey: "song:cn:1", Status: domain.JobCompleted, CreatedAt: base, UpdatedAt: base},
		{ID: "queued", Input: "https://music.apple.com/cn/song/queued/2", Type: "song", Storefront: "cn", CanonicalKey: "song:cn:2", Status: domain.JobQueued, CreatedAt: base.Add(time.Second), UpdatedAt: base.Add(time.Second)},
		{ID: "running", Input: "https://music.apple.com/cn/song/running/3", Type: "song", Storefront: "cn", CanonicalKey: "song:cn:3", Status: domain.JobRunning, CreatedAt: base.Add(2 * time.Second), UpdatedAt: base.Add(2 * time.Second)},
		{ID: "failed", Input: "https://music.apple.com/cn/song/failed/4", Type: "song", Storefront: "cn", CanonicalKey: "song:cn:4", Status: domain.JobFailed, CreatedAt: base.Add(3 * time.Second), UpdatedAt: base.Add(3 * time.Second)},
		{ID: "cancelled", Input: "https://music.apple.com/cn/song/cancelled/5", Type: "song", Storefront: "cn", CanonicalKey: "song:cn:5", Status: domain.JobCancelled, CreatedAt: base.Add(4 * time.Second), UpdatedAt: base.Add(4 * time.Second)},
	}
	for _, job := range jobs {
		if err := store.CreateJob(ctx, job); err != nil {
			t.Fatal(err)
		}
	}

	got, err := store.ListRecoverableJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d recoverable jobs, want 2: %+v", len(got), got)
	}
	if got[0].ID != "queued" || got[1].ID != "running" {
		t.Fatalf("recoverable job order/ids = [%s, %s], want [queued, running]", got[0].ID, got[1].ID)
	}
}
