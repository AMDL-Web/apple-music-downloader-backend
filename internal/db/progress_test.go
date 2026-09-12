package db

import (
	"context"
	"fmt"
	"testing"

	"amdl/internal/domain"
)

func TestCountItemProgressMatchesItems(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	statuses := []domain.ItemStatus{
		domain.ItemQueued, domain.ItemResolving, domain.ItemWaitingDownload,
		domain.ItemDownloading, domain.ItemWaitingDecrypt, domain.ItemDecrypting,
		domain.ItemRemuxing, domain.ItemTagging, domain.ItemSaving,
		domain.ItemCompleted, domain.ItemSkipped, domain.ItemFailed, domain.ItemCancelled, "future_status",
	}
	for i, status := range statuses {
		if err := store.CreateItem(ctx, domain.JobItem{ID: fmt.Sprint(i), JobID: "job", Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CreateItem(ctx, domain.JobItem{ID: "other", JobID: "other", Status: domain.ItemCompleted}); err != nil {
		t.Fatal(err)
	}
	check := func(jobID string) {
		t.Helper()
		items, err := store.ListItems(ctx, jobID)
		if err != nil {
			t.Fatal(err)
		}
		wantDone, wantFailed := domain.CountItemProgress(items)
		done, failed, err := store.CountItemProgress(ctx, jobID)
		if err != nil || done != wantDone || failed != wantFailed {
			t.Fatalf("%s: (%d, %d, %v), want (%d, %d)", jobID, done, failed, err, wantDone, wantFailed)
		}
	}
	check("job")
	check("other")
	check("empty")
	if err := store.UpdateItem(ctx, domain.JobItem{ID: "0", Status: domain.ItemCompleted}); err != nil {
		t.Fatal(err)
	}
	check("job") // Counters reflect live transitions, without a job-row refresh.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := store.CountItemProgress(cancelled, "job"); err == nil {
		t.Fatal("cancelled query succeeded")
	}
}

// Compare only the counter read; fixture setup is excluded from timing.
func BenchmarkItemProgress(b *testing.B) {
	store, err := Open(":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	for i := 0; i < 1000; i++ {
		if err := store.CreateItem(ctx, domain.JobItem{ID: fmt.Sprint(i), JobID: "job", Status: domain.ItemCompleted}); err != nil {
			b.Fatal(err)
		}
	}
	b.Run("ListItems", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			items, err := store.ListItems(ctx, "job")
			if err != nil {
				b.Fatal(err)
			}
			if done, _ := domain.CountItemProgress(items); done != 1000 {
				b.Fatal(done)
			}
		}
	})
	b.Run("Aggregate", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			done, _, err := store.CountItemProgress(ctx, "job")
			if err != nil || done != 1000 {
				b.Fatalf("done %d: %v", done, err)
			}
		}
	})
}
