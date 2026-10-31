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
		ID: "item-1", JobID: "job-1", AdamID: "123", Kind: "song", Index: 1, DurationMS: 245000,
		Status: domain.ItemDownloading, Codec: "alac", BitDepth: 24, SampleRate: 96000, Bitrate: 2500000, FileSize: 34567890,
		RetryKind: "codec", Attempt: 2, MaxAttempts: 4,
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
	if got.Codec != want.Codec || got.BitDepth != want.BitDepth || got.SampleRate != want.SampleRate || got.Bitrate != want.Bitrate {
		t.Fatalf("quality = %+v, want %+v", got, want)
	}
	if got.FileSize != want.FileSize {
		t.Fatalf("file_size = %d, want %d", got.FileSize, want.FileSize)
	}
	if got.DurationMS != want.DurationMS {
		t.Fatalf("duration_ms = %d, want %d", got.DurationMS, want.DurationMS)
	}

	// Falling back to aac-lc, which has no per-track manifest to read quality
	// from, must clear the previous codec's quality rather than carry it over.
	updated := got
	updated.Codec, updated.BitDepth, updated.SampleRate, updated.Bitrate = "aac-lc", 0, 0, 0
	if err := store.UpdateItem(context.Background(), updated); err != nil {
		t.Fatal(err)
	}
	items, err = store.ListItems(context.Background(), want.JobID)
	if err != nil {
		t.Fatal(err)
	}
	got = items[0]
	if got.Codec != "aac-lc" || got.BitDepth != 0 || got.SampleRate != 0 || got.Bitrate != 0 {
		t.Fatalf("quality after UpdateItem = %+v, want codec=aac-lc bit_depth=0 sample_rate=0 bitrate=0", got)
	}
}

func TestJobItemHasLyricsRoundTrip(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	want := domain.JobItem{
		ID: "item-lyrics", JobID: "job-1", AdamID: "123", Kind: "song", Index: 1,
		HasLyrics: true, Status: domain.ItemQueued, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateItem(ctx, want); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListItems(ctx, want.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !items[0].HasLyrics {
		t.Fatalf("items = %+v, want one item with has_lyrics=true", items)
	}

	// The per-track metadata refresh may correct the flag mid-job; UpdateItem
	// must persist the new value, along with the lyrics fetch outcome.
	updated := items[0]
	updated.HasLyrics = false
	updated.LyricsStatus = domain.LyricsFailed
	if err := store.UpdateItem(ctx, updated); err != nil {
		t.Fatal(err)
	}
	items, err = store.ListItems(ctx, want.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].HasLyrics || items[0].LyricsStatus != domain.LyricsFailed {
		t.Fatalf("items after UpdateItem = %+v, want has_lyrics=false lyrics_status=failed", items)
	}

	// A retry reset clears the per-attempt lyrics outcome (the next attempt
	// may succeed) but keeps has_lyrics, which is resolved catalog metadata.
	updated = items[0]
	updated.HasLyrics = true
	updated.Status = domain.ItemFailed
	if err := store.UpdateItem(ctx, updated); err != nil {
		t.Fatal(err)
	}
	if err := store.ResetUnfinishedItems(ctx, want.JobID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	items, err = store.ListItems(ctx, want.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != domain.ItemQueued || items[0].LyricsStatus != domain.LyricsPending || !items[0].HasLyrics {
		t.Fatalf("items after reset = %+v, want queued with lyrics_status cleared and has_lyrics kept", items)
	}
}

func TestJobItemProgressRoundTrip(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	// A half-decrypted item: both meters carry a fraction and only the earlier
	// stages are marked, so a round-trip that silently collapsed the breakdown
	// into one value could not reproduce it.
	want := domain.ItemProgress{Download: 1, Decrypt: 0.42, DownloadBytes: 31_457_280, Resolved: true}
	item := domain.JobItem{
		ID: "item-1", JobID: "job-1", AdamID: "123", Kind: "song", Index: 1,
		Status: domain.ItemDecrypting, Progress: want, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListItems(ctx, item.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if got := items[0].Progress; got != want {
		t.Fatalf("progress after CreateItem = %+v, want %+v", got, want)
	}

	updated := items[0]
	updated.Status = domain.ItemCompleted
	updated.Progress = domain.ItemProgress{Download: 1, Decrypt: 1, DownloadBytes: 31_457_280, Resolved: true, Remuxed: true, Verified: true, Tagged: true, Saved: true}
	if err := store.UpdateItem(ctx, updated); err != nil {
		t.Fatal(err)
	}
	items, err = store.ListItems(ctx, item.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if got := items[0].Progress; got != updated.Progress {
		t.Fatalf("progress after UpdateItem = %+v, want %+v", got, updated.Progress)
	}

	// ResetUnfinishedItems must clear every breakdown column, matching
	// domain.JobItem.ResetForRetry. A completed item is left alone, so reset a
	// non-finished one.
	updated.Status = domain.ItemFailed
	if err := store.UpdateItem(ctx, updated); err != nil {
		t.Fatal(err)
	}
	if err := store.ResetUnfinishedItems(ctx, item.JobID, now); err != nil {
		t.Fatal(err)
	}
	items, err = store.ListItems(ctx, item.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if got := items[0].Progress; got != (domain.ItemProgress{}) {
		t.Fatalf("progress after ResetUnfinishedItems = %+v, want zero", got)
	}
}
