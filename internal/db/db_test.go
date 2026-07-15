package db

import (
	"context"
	"database/sql"
	"errors"
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
		Status: domain.ItemDownloading, Codec: "alac", BitDepth: 24, SampleRate: 96000, Bitrate: 2500000,
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

func TestFinalizeJobPersistsStatusAndEventTogether(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	job := domain.Job{ID: "job-finalize", Input: "song|us|1", Type: "song", Status: domain.JobRunning, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	job.Status = domain.JobCompleted
	job.UpdatedAt = now.Add(time.Second)
	ev := domain.Event{JobID: job.ID, Type: "job_finished", Message: string(job.Status)}
	stored, err := store.FinalizeJob(ctx, job, ev)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID == 0 {
		t.Fatal("FinalizeJob did not assign an event id")
	}

	got, err := store.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.JobCompleted {
		t.Fatalf("job status = %s, want %s", got.Status, domain.JobCompleted)
	}
	events, err := store.ListEventsAfter(ctx, job.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "job_finished" {
		t.Fatalf("events after FinalizeJob = %+v, want a single job_finished event", events)
	}
}

func TestListEventsAfterLimitPagesWithoutSkipping(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	job := domain.Job{ID: "job-page", Input: "song|us|1", Type: "song", Status: domain.JobQueued}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := store.AddEvent(ctx, domain.Event{JobID: job.ID, Type: "item_progress"}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.ListEventsAfterLimit(ctx, job.ID, 0, 2)
	if err != nil || len(first) != 2 {
		t.Fatalf("first page = (%+v, %v), want 2 events", first, err)
	}
	second, err := store.ListEventsAfterLimit(ctx, job.ID, first[1].ID, 2)
	if err != nil || len(second) != 1 || second[0].ID <= first[1].ID {
		t.Fatalf("second page = (%+v, %v), want final event after cursor", second, err)
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

func TestArtworkURLRoundTrip(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	job := domain.Job{
		ID: "job-art", Input: "https://music.apple.com/cn/album/test/1", Type: "album", Storefront: "cn",
		Status: domain.JobQueued, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	// Artwork is written back after the input resolves, via UpdateJob.
	job.ArtworkURL = "https://is1-ssl.mzstatic.com/image/thumb/Music/album/{w}x{h}bb.jpg"
	job.Status = domain.JobRunning
	if err := store.UpdateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ArtworkURL != job.ArtworkURL {
		t.Fatalf("job artwork_url = %q, want %q", got.ArtworkURL, job.ArtworkURL)
	}
	listed, total, err := store.ListJobs(ctx, JobListFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("total = %d, want 1", total)
	}
	if len(listed) != 1 || listed[0].ArtworkURL != job.ArtworkURL {
		t.Fatalf("listed job artwork_url = %+v, want %q", listed, job.ArtworkURL)
	}

	item := domain.JobItem{
		ID: "item-art", JobID: job.ID, AdamID: "123", Kind: "song", Index: 1,
		ArtworkURL: "https://is1-ssl.mzstatic.com/image/thumb/Music/track/{w}x{h}bb.jpg",
		Status:     domain.ItemQueued, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	item.ArtworkURL = "https://is1-ssl.mzstatic.com/image/thumb/Music/track-refreshed/{w}x{h}bb.jpg"
	if err := store.UpdateItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListItems(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ArtworkURL != item.ArtworkURL {
		t.Fatalf("item artwork_url = %+v, want %q", items, item.ArtworkURL)
	}
}

func TestOpenMigratesExistingSchemaWithoutArtworkColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "amdl.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// Pre-artwork_url schema as created by older builds.
	stmts := []string{
		`CREATE TABLE jobs (
			id TEXT PRIMARY KEY,
			input TEXT NOT NULL,
			type TEXT NOT NULL,
			storefront TEXT,
			canonical_key TEXT NOT NULL,
			force INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL,
			total_items INTEGER NOT NULL DEFAULT 0,
			done_items INTEGER NOT NULL DEFAULT 0,
			failed_items INTEGER NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE job_items (
			id TEXT PRIMARY KEY,
			job_id TEXT NOT NULL,
			adam_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			idx INTEGER NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			artist TEXT NOT NULL DEFAULT '',
			album TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			progress REAL NOT NULL DEFAULT 0,
			codec TEXT NOT NULL DEFAULT '',
			retry_kind TEXT NOT NULL DEFAULT '',
			attempt INTEGER NOT NULL DEFAULT 0,
			max_attempts INTEGER NOT NULL DEFAULT 0,
			status_message TEXT NOT NULL DEFAULT '',
			output_path TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`INSERT INTO jobs(id,input,type,storefront,canonical_key,force,status,created_at,updated_at)
			VALUES('job-old','https://music.apple.com/cn/song/old/1','song','cn','song:cn:1',0,'completed','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z');`,
		`INSERT INTO job_items(id,job_id,adam_id,kind,idx,status,created_at,updated_at)
			VALUES('item-old','job-old','1','song',1,'completed','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z');`,
	}
	for _, stmt := range stmts {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	// Open must add the artwork_url columns without touching existing rows,
	// and reopening must be idempotent.
	for i := 0; i < 2; i++ {
		store, err := Open(path)
		if err != nil {
			t.Fatalf("open #%d: %v", i+1, err)
		}
		ctx := context.Background()
		got, err := store.GetJob(ctx, "job-old")
		if err != nil {
			t.Fatal(err)
		}
		if got.ArtworkURL != "" {
			t.Fatalf("legacy job artwork_url = %q, want empty", got.ArtworkURL)
		}
		items, err := store.ListItems(ctx, "job-old")
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || items[0].ArtworkURL != "" {
			t.Fatalf("legacy items = %+v, want one item with empty artwork_url", items)
		}
		if items[0].HasLyrics {
			t.Fatalf("legacy item has_lyrics = true, want false for rows created before the column existed")
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}

	// The migrated columns must be writable.
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	job, err := store.GetJob(ctx, "job-old")
	if err != nil {
		t.Fatal(err)
	}
	job.ArtworkURL = "https://is1-ssl.mzstatic.com/image/thumb/Music/old/{w}x{h}bb.jpg"
	if err := store.UpdateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ArtworkURL != job.ArtworkURL {
		t.Fatalf("migrated job artwork_url = %q, want %q", got.ArtworkURL, job.ArtworkURL)
	}
	items, err := store.ListItems(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	item := items[0]
	item.HasLyrics = true
	if err := store.UpdateItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	items, err = store.ListItems(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !items[0].HasLyrics {
		t.Fatal("migrated item has_lyrics not persisted by UpdateItem")
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

func TestListMilestoneEventsAfterFiltersAndOrders(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	for _, job := range []domain.Job{
		{ID: "job1", Input: "song|us|1", Type: "song", CanonicalKey: "song:us:1", Status: domain.JobRunning},
		{ID: "job2", Input: "song|us|2", Type: "song", CanonicalKey: "song:us:2", Status: domain.JobRunning},
	} {
		if err := store.CreateJob(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	// Mix milestone and non-milestone events across two jobs.
	for _, ev := range []domain.Event{
		{JobID: "job1", Type: "job_started"},    // milestone
		{JobID: "job1", Type: "codec_selected"}, // not a milestone
		{JobID: "job2", Type: "job_queued"},     // milestone
		{JobID: "job1", Type: "item_completed"}, // milestone
	} {
		if _, err := store.AddEvent(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}

	got, err := store.ListMilestoneEventsAfter(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantTypes := []string{"job_started", "job_queued", "item_completed"}
	if len(got) != len(wantTypes) {
		t.Fatalf("got %d milestone events, want %d: %+v", len(got), len(wantTypes), got)
	}
	for i, ty := range wantTypes {
		if got[i].Type != ty {
			t.Fatalf("milestone[%d].Type = %s, want %s (codec_selected must be filtered out, order by id)", i, got[i].Type, ty)
		}
	}

	// The global cursor is the max id across all jobs.
	last, err := store.LatestGlobalEventID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if last != got[len(got)-1].ID {
		t.Fatalf("LatestGlobalEventID = %d, want %d", last, got[len(got)-1].ID)
	}

	// Resuming from that cursor yields nothing new.
	after, err := store.ListMilestoneEventsAfter(ctx, last)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatalf("ListMilestoneEventsAfter(last) = %+v, want empty", after)
	}
}

func TestDeleteJobPersistsOverviewTombstone(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	job := domain.Job{ID: "job1", Input: "song|us|1", Type: "song", CanonicalKey: "song:us:1", Status: domain.JobCompleted}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	finished, err := store.AddEvent(ctx, domain.Event{JobID: job.ID, Type: "job_finished"})
	if err != nil {
		t.Fatal(err)
	}
	tombstone, err := store.DeleteJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tombstone.Type != domain.EventDeleted || tombstone.JobID != job.ID || tombstone.ID <= finished.ID {
		t.Fatalf("tombstone = %+v, want job_deleted for %s after event %d", tombstone, job.ID, finished.ID)
	}

	events, err := store.ListMilestoneEventsAfter(ctx, finished.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != domain.EventDeleted || events[0].ID != tombstone.ID {
		t.Fatalf("ListMilestoneEventsAfter(%d) = %+v, want tombstone %d", finished.ID, events, tombstone.ID)
	}
	if _, err := store.GetJob(ctx, job.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetJob after delete err = %v, want sql.ErrNoRows", err)
	}
}

func TestListJobsFilterPaginationAndSort(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	base := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	jobs := []domain.Job{
		{ID: "j1", Input: "https://music.apple.com/us/song/one/1", Type: "song", Storefront: "us", Title: "Alpha Song", CanonicalKey: "song|us|1", Status: domain.JobCompleted, CreatedAt: base, UpdatedAt: base.Add(3 * time.Hour)},
		{ID: "j2", Input: "https://music.apple.com/cn/album/two/2", Type: "album", Storefront: "cn", Title: "Beta Album", CanonicalKey: "album|cn|2", Status: domain.JobFailed, CreatedAt: base.Add(1 * time.Hour), UpdatedAt: base.Add(2 * time.Hour)},
		{ID: "j3", Input: "https://music.apple.com/jp/playlist/three/3", Type: "playlist", Storefront: "jp", Title: "Gamma Playlist", CanonicalKey: "playlist|jp|3", Status: domain.JobRunning, CreatedAt: base.Add(2 * time.Hour), UpdatedAt: base.Add(4 * time.Hour)},
		{ID: "j4", Input: "https://music.apple.com/us/artist/four/4", Type: "artist", Storefront: "us", Title: "Delta Artist", CanonicalKey: "artist|us|4", Status: domain.JobQueued, CreatedAt: base.Add(3 * time.Hour), UpdatedAt: base.Add(1 * time.Hour)},
	}
	for _, job := range jobs {
		if err := store.CreateJob(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CreateItem(ctx, domain.JobItem{
		ID: "i1", JobID: "j3", AdamID: "1", Kind: "song", Index: 1,
		Status: domain.ItemCompleted, CreatedAt: base, UpdatedAt: base,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateItem(ctx, domain.JobItem{
		ID: "i2", JobID: "j3", AdamID: "2", Kind: "song", Index: 2,
		Status: domain.ItemFailed, CreatedAt: base, UpdatedAt: base,
	}); err != nil {
		t.Fatal(err)
	}

	listed, total, err := store.ListJobs(ctx, JobListFilter{Limit: 2, Offset: 1, Sort: JobListSortCreatedAt, Order: JobListOrderDesc})
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 {
		t.Fatalf("total = %d, want 4", total)
	}
	if len(listed) != 2 || listed[0].ID != "j3" || listed[1].ID != "j2" {
		t.Fatalf("page = %+v, want j3 then j2", idsOf(listed))
	}
	if listed[0].DoneItems != 1 || listed[0].FailedItems != 1 {
		t.Fatalf("j3 progress done=%d failed=%d, want 1/1", listed[0].DoneItems, listed[0].FailedItems)
	}

	listed, total, err = store.ListJobs(ctx, JobListFilter{
		Statuses: []domain.JobStatus{domain.JobFailed, domain.JobCancelled},
		Limit:    50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(listed) != 1 || listed[0].ID != "j2" {
		t.Fatalf("status filter = ids=%v total=%d, want [j2]/1", idsOf(listed), total)
	}

	listed, total, err = store.ListJobs(ctx, JobListFilter{Types: []string{"song", "artist"}, Storefront: "us", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(listed) != 2 {
		t.Fatalf("type+storefront = ids=%v total=%d, want 2 us song/artist", idsOf(listed), total)
	}

	listed, total, err = store.ListJobs(ctx, JobListFilter{Query: "beta", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || listed[0].ID != "j2" {
		t.Fatalf("q=beta = ids=%v total=%d, want [j2]", idsOf(listed), total)
	}

	listed, total, err = store.ListJobs(ctx, JobListFilter{Query: "%", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 {
		t.Fatalf("literal %% query matched %d jobs, want 0", total)
	}

	after := base.Add(90 * time.Minute)
	before := base.Add(150 * time.Minute)
	listed, total, err = store.ListJobs(ctx, JobListFilter{CreatedAfter: &after, CreatedBefore: &before, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || listed[0].ID != "j3" {
		t.Fatalf("created window = ids=%v total=%d, want [j3]", idsOf(listed), total)
	}

	listed, total, err = store.ListJobs(ctx, JobListFilter{Sort: JobListSortUpdatedAt, Order: JobListOrderAsc, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 || idsOf(listed)[0] != "j4" || idsOf(listed)[3] != "j3" {
		t.Fatalf("sort updated_at asc = %v, want j4 ... j3", idsOf(listed))
	}
}

// TestListJobsWholeSecondBoundaryWithFractionalTimes guards the lexicographic
// TEXT compare bug: a whole-second filter like created_after=...T12:00:00Z must
// still include rows stored with sub-second fractions in that same second, and
// ORDER BY must keep chronological order across whole-second vs fractional rows.
func TestListJobsWholeSecondBoundaryWithFractionalTimes(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	sec := time.Date(2024, 7, 10, 12, 0, 0, 0, time.UTC)
	jobs := []domain.Job{
		{ID: "exact", Input: "song|us|exact", Type: "song", CanonicalKey: "song|us|exact", Status: domain.JobCompleted, CreatedAt: sec, UpdatedAt: sec},
		{ID: "frac", Input: "song|us|frac", Type: "song", CanonicalKey: "song|us|frac", Status: domain.JobCompleted, CreatedAt: sec.Add(123 * time.Millisecond), UpdatedAt: sec.Add(123 * time.Millisecond)},
		{ID: "next", Input: "song|us|next", Type: "song", CanonicalKey: "song|us|next", Status: domain.JobCompleted, CreatedAt: sec.Add(time.Second), UpdatedAt: sec.Add(time.Second)},
	}
	for _, job := range jobs {
		if err := store.CreateJob(ctx, job); err != nil {
			t.Fatal(err)
		}
	}

	// Persist via CreateJob uses formatTime; assert fixed-width storage.
	var stored string
	if err := store.db.QueryRowContext(ctx, `SELECT created_at FROM jobs WHERE id='frac'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	wantStored := "2024-07-10T12:00:00.123000000Z"
	if stored != wantStored {
		t.Fatalf("stored created_at = %q, want fixed-width %q", stored, wantStored)
	}

	after := sec
	listed, total, err := store.ListJobs(ctx, JobListFilter{CreatedAfter: &after, Sort: JobListSortCreatedAt, Order: JobListOrderAsc, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || idsOf(listed)[0] != "exact" || idsOf(listed)[1] != "frac" || idsOf(listed)[2] != "next" {
		t.Fatalf("created_after whole-second = ids=%v total=%d, want [exact frac next]", idsOf(listed), total)
	}

	before := sec
	listed, total, err = store.ListJobs(ctx, JobListFilter{CreatedBefore: &before, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || listed[0].ID != "exact" {
		t.Fatalf("created_before whole-second = ids=%v total=%d, want [exact] only (frac must be excluded)", idsOf(listed), total)
	}
}

func TestNormalizeTimestampColumnsOnOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "amdl.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`CREATE TABLE jobs (
			id TEXT PRIMARY KEY, input TEXT NOT NULL, type TEXT NOT NULL, storefront TEXT,
			title TEXT NOT NULL DEFAULT '', artwork_url TEXT NOT NULL DEFAULT '',
			canonical_key TEXT NOT NULL, force INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL,
			total_items INTEGER NOT NULL DEFAULT 0, done_items INTEGER NOT NULL DEFAULT 0,
			failed_items INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE job_items (
			id TEXT PRIMARY KEY, job_id TEXT NOT NULL, adam_id TEXT NOT NULL, kind TEXT NOT NULL,
			idx INTEGER NOT NULL, title TEXT NOT NULL DEFAULT '', artist TEXT NOT NULL DEFAULT '',
			album TEXT NOT NULL DEFAULT '', artwork_url TEXT NOT NULL DEFAULT '', status TEXT NOT NULL,
			progress REAL NOT NULL DEFAULT 0, codec TEXT NOT NULL DEFAULT '',
			bit_depth INTEGER NOT NULL DEFAULT 0, sample_rate INTEGER NOT NULL DEFAULT 0,
			bitrate INTEGER NOT NULL DEFAULT 0, retry_kind TEXT NOT NULL DEFAULT '',
			attempt INTEGER NOT NULL DEFAULT 0, max_attempts INTEGER NOT NULL DEFAULT 0,
			status_message TEXT NOT NULL DEFAULT '', output_path TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE job_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT, job_id TEXT NOT NULL, item_id TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL, phase TEXT NOT NULL DEFAULT '', message TEXT NOT NULL DEFAULT '',
			payload TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL
		);`,
		// Legacy variable-width RFC3339Nano (whole second + trimmed millis).
		`INSERT INTO jobs(id,input,type,storefront,canonical_key,force,status,created_at,updated_at)
			VALUES('j1','u','song','us','k',0,'completed','2024-07-10T12:00:00Z','2024-07-10T12:00:00.123Z');`,
		`INSERT INTO job_items(id,job_id,adam_id,kind,idx,status,created_at,updated_at)
			VALUES('i1','j1','1','song',1,'completed','2024-07-10T12:00:00.5Z','2024-07-10T12:00:00.5Z');`,
		`INSERT INTO job_events(job_id,type,created_at) VALUES('j1','job_finished','2024-07-10T12:00:01Z');`,
	}
	for _, stmt := range stmts {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	checks := []struct {
		query string
		want  string
	}{
		{`SELECT created_at FROM jobs WHERE id='j1'`, "2024-07-10T12:00:00.000000000Z"},
		{`SELECT updated_at FROM jobs WHERE id='j1'`, "2024-07-10T12:00:00.123000000Z"},
		{`SELECT created_at FROM job_items WHERE id='i1'`, "2024-07-10T12:00:00.500000000Z"},
		{`SELECT created_at FROM job_events WHERE job_id='j1'`, "2024-07-10T12:00:01.000000000Z"},
	}
	for _, c := range checks {
		var got string
		if err := store.db.QueryRow(c.query).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Fatalf("%s = %q, want %q", c.query, got, c.want)
		}
	}

	// Idempotent: reopen must not fail or change already-normalized rows.
	store.Close()
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var got string
	if err := store.db.QueryRow(`SELECT created_at FROM jobs WHERE id='j1'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "2024-07-10T12:00:00.000000000Z" {
		t.Fatalf("after reopen created_at = %q", got)
	}
}

func TestFormatTimeFixedWidth(t *testing.T) {
	exact := time.Date(2024, 7, 10, 12, 0, 0, 0, time.UTC)
	frac := time.Date(2024, 7, 10, 12, 0, 0, 123000000, time.UTC)
	if got, want := formatTime(exact), "2024-07-10T12:00:00.000000000Z"; got != want {
		t.Fatalf("formatTime(exact) = %q, want %q", got, want)
	}
	if got, want := formatTime(frac), "2024-07-10T12:00:00.123000000Z"; got != want {
		t.Fatalf("formatTime(frac) = %q, want %q", got, want)
	}
	// Lexicographic order must match chronological order across the boundary.
	if !(formatTime(exact) < formatTime(frac) && formatTime(frac) < formatTime(exact.Add(time.Second))) {
		t.Fatalf("fixed-width strings are not chronologically ordered: %q %q %q",
			formatTime(exact), formatTime(frac), formatTime(exact.Add(time.Second)))
	}
}

func idsOf(jobs []domain.Job) []string {
	out := make([]string, len(jobs))
	for i, job := range jobs {
		out[i] = job.ID
	}
	return out
}
