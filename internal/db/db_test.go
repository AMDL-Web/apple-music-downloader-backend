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

func TestJobListFilterMatches(t *testing.T) {
	base := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	job := domain.Job{
		ID: "j1", Input: "https://music.apple.com/us/song/alpha/1", Type: "song",
		Storefront: "us", Title: "Alpha Song", Status: domain.JobRunning,
		CreatedAt: base, UpdatedAt: base.Add(time.Hour),
	}
	empty := JobListFilter{}
	if !empty.Matches(job) || empty.HasConstraints() {
		t.Fatalf("empty filter should match everything and report no constraints")
	}
	statusOnly := JobListFilter{Statuses: []domain.JobStatus{domain.JobFailed}}
	if statusOnly.Matches(job) || !statusOnly.HasConstraints() {
		t.Fatalf("status filter should reject running job")
	}
	typeSF := JobListFilter{Types: []string{"song"}, Storefront: "us"}
	if !typeSF.Matches(job) {
		t.Fatalf("type+storefront should match")
	}
	q := JobListFilter{Query: "ALPHA"}
	if !q.Matches(job) {
		t.Fatalf("q should be case-insensitive")
	}
	after := base.Add(2 * time.Hour)
	window := JobListFilter{CreatedAfter: &after}
	if window.Matches(job) {
		t.Fatalf("created_after should reject older job")
	}
}

func idsOf(jobs []domain.Job) []string {
	out := make([]string, len(jobs))
	for i, job := range jobs {
		out[i] = job.ID
	}
	return out
}
