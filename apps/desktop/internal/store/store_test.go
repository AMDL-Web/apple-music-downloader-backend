package store

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"

	"amdl/internal/client"
	"amdl/internal/domain"
)

type recorder struct{ finished []string }

func (r *recorder) JobFinished(j *Job) { r.finished = append(r.finished, j.ID+":"+string(j.Status)) }

// newTestStore has no client, so it starts no goroutines: the apply methods
// run as the UI thread would run them.
func newTestStore(t *testing.T) (*Store, *recorder) {
	t.Helper()
	rec := &recorder{}
	s := New(func(fn func()) { fn() }, rec)
	return s, rec
}

func at(min int) time.Time { return time.Date(2026, 10, 30, 10, min, 0, 0, time.Local) }

func TestSnapshotAndFeedKeepBackendOrder(t *testing.T) {
	s, _ := newTestStore(t)
	// A batch submitted together shares created_at and lists in submission order.
	s.ApplySnapshot(client.DownloadList{Downloads: []domain.Job{
		{ID: "b1", CreatedAt: at(5)}, {ID: "b2", CreatedAt: at(5)}, {ID: "old", CreatedAt: at(1), Status: domain.JobCompleted},
	}, StatusCounts: domain.JobStatusCounts{Completed: 1, Queued: 2, Total: 3}})
	s.applyMessage(domain.DownloadFeedMessage{Type: "download_upserted", Job: &domain.Job{ID: "new", CreatedAt: at(9), Status: domain.JobQueued},
		StatusCounts: domain.JobStatusCounts{Completed: 1, Queued: 3, Total: 4}})
	var ids []string
	for _, j := range s.Jobs {
		ids = append(ids, j.ID)
	}
	if fmt.Sprint(ids) != "[new b1 b2 old]" {
		t.Errorf("order %v", ids)
	}
	if s.Count(TabActive) != 3 || s.Count(TabAll) != 4 {
		t.Errorf("counts %+v", s.Counts)
	}
	s.applyMessage(domain.DownloadFeedMessage{Type: "download_deleted", JobID: "b1"})
	if s.Job("b1") != nil || len(s.Jobs) != 3 {
		t.Error("deleted job still listed")
	}
}

func TestNotifiesOnlyRunsSeenUnfinished(t *testing.T) {
	s, rec := newTestStore(t)
	s.ApplySnapshot(client.DownloadList{Downloads: []domain.Job{
		{ID: "done-before", Status: domain.JobCompleted}, {ID: "run", Status: domain.JobRunning},
	}})
	upsert := func(id string, st domain.JobStatus) {
		s.applyMessage(domain.DownloadFeedMessage{Type: "download_upserted", Job: &domain.Job{ID: id, Status: st}})
	}
	upsert("done-before", domain.JobCompleted) // a late motion-cover update of a finished job
	upsert("run", domain.JobCompleted)
	upsert("run", domain.JobCompleted) // the same state again
	// A per-job lifecycle event can carry the end before the feed does.
	s.applyEvent("run", domain.Event{ID: 99, Type: domain.EventJobFinished, Payload: mustJSON(domain.Job{ID: "run", Status: domain.JobCompleted})})
	upsert("run", domain.JobQueued) // retried: a new run
	upsert("run", domain.JobFailed)
	upsert("cancelled", domain.JobRunning)
	upsert("cancelled", domain.JobCancelled)
	if want := "[run:completed run:failed]"; fmt.Sprint(rec.finished) != want {
		t.Errorf("notified %v, want %s", rec.finished, want)
	}
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

func TestEventsOlderThanSnapshotOnlyLog(t *testing.T) {
	s, _ := newTestStore(t)
	s.ApplySnapshot(client.DownloadList{Downloads: []domain.Job{{ID: "j", Status: domain.JobRunning, TotalItems: 1}}})
	s.applyDetail("j", client.DownloadDetail{
		Job:         domain.Job{ID: "j", Status: domain.JobRunning, TotalItems: 1},
		Items:       []domain.JobItem{{ID: "i", Index: 1, Status: domain.ItemDecrypting, Progress: domain.ItemProgress{Resolved: true, Download: 1, Decrypt: 0.5}}},
		EventCursor: 10,
	})
	old := domain.JobItem{ID: "i", Index: 1, Status: domain.ItemDownloading, Progress: domain.ItemProgress{Resolved: true, Download: 0.2}}
	s.applyEvent("j", domain.Event{ID: 5, ItemID: "i", Type: domain.EventItemProgress, Payload: mustJSON(old)})
	if got := s.Job("j").Items[0].Status; got != domain.ItemDecrypting {
		t.Fatalf("history overwrote the snapshot: status %s", got)
	}
	done := domain.JobItem{ID: "i", Index: 1, Status: domain.ItemCompleted, FileSize: 41_000_000}
	s.applyEvent("j", domain.Event{ID: 11, ItemID: "i", Type: domain.EventItemCompleted, Payload: mustJSON(done)})
	j := s.Job("j")
	if j.Items[0].Status != domain.ItemCompleted || math.Abs(j.Progress-1) > 1e-9 {
		t.Fatalf("item %+v, progress %v", j.Items[0], j.Progress)
	}
	if len(j.Activity) != 1 || j.Activity[0].Text != "Track 1 saved · 41.0 MB" {
		t.Errorf("activity %+v", j.Activity)
	}
}

func TestNowIsTheLatestStartedRunningJob(t *testing.T) {
	s, _ := newTestStore(t)
	early, late := at(1), at(7)
	s.ApplySnapshot(client.DownloadList{Downloads: []domain.Job{
		{ID: "a", Status: domain.JobRunning, StartedAt: &early, CreatedAt: at(9)},
		{ID: "b", Status: domain.JobRunning, StartedAt: &late, CreatedAt: at(0)},
		{ID: "c", Status: domain.JobQueued, CreatedAt: at(10)},
	}})
	if n := s.Now(); n == nil || n.ID != "b" {
		t.Fatalf("now = %v", n)
	}
}

func TestSummaryCountsToday(t *testing.T) {
	s, _ := newTestStore(t)
	s.now = func() time.Time { return at(30) }
	yesterday, today := at(30).Add(-24*time.Hour), at(2)
	s.ApplySnapshot(client.DownloadList{Downloads: []domain.Job{
		{ID: "r", Status: domain.JobRunning, TotalItems: 4, CompletedItems: 1, SkippedItems: 1},
		{ID: "f1", Status: domain.JobFailed, FinishedAt: &today},
		{ID: "f0", Status: domain.JobFailed, FinishedAt: &yesterday},
		{ID: "q", Status: domain.JobQueued},
	}})
	sum := s.Summary()
	if sum.Running != 1 || sum.Queued != 1 || sum.TracksDone != 2 || sum.TracksTotal != 4 || sum.FailedToday != 1 {
		t.Errorf("summary %+v", sum)
	}
	if math.Abs(sum.Progress-0.5) > 1e-9 {
		t.Errorf("progress %v, want 0.5 from the counters", sum.Progress)
	}
}

func TestFindLinksAndRunningMatch(t *testing.T) {
	text := "IGOR https://music.apple.com/us/album/igor/1463409338, and\nhttps://music.apple.com/cn/album/1858184006 again https://music.apple.com/us/album/igor/1463409338"
	links := FindLinks(text)
	if fmt.Sprint(links) != "[https://music.apple.com/us/album/igor/1463409338 https://music.apple.com/cn/album/1858184006]" {
		t.Fatalf("links %q", links)
	}
	s, _ := newTestStore(t)
	s.ApplySnapshot(client.DownloadList{Downloads: []domain.Job{
		{ID: "j", Status: domain.JobRunning, Input: "https://music.apple.com/cn/album/x/1858184006?i=1858184010"},
	}})
	l := &Link{State: LinkReady, Lookup: client.Lookup{Type: "album", ID: "1858184006"}}
	if j := s.Running(l); j == nil || j.ID != "j" {
		t.Errorf("running = %v", j)
	}
	l.Lookup.ID = "1463409338"
	if s.Running(l) != nil {
		t.Error("an album nobody downloads matched")
	}
}

func TestItemEventsRecountTracks(t *testing.T) {
	s, _ := newTestStore(t)
	stale := domain.Job{ID: "j", Status: domain.JobRunning, TotalItems: 2}
	s.ApplySnapshot(client.DownloadList{Downloads: []domain.Job{stale}})
	s.applyDetail("j", client.DownloadDetail{
		Job:         stale,
		Items:       []domain.JobItem{{ID: "a", Index: 1, Status: domain.ItemDownloading}, {ID: "b", Index: 2, Status: domain.ItemDownloading}},
		EventCursor: 10,
	})
	// Item events carry only the track; the job's counters must follow it.
	s.applyEvent("j", domain.Event{ID: 11, ItemID: "a", Type: domain.EventItemCompleted, Payload: mustJSON(domain.JobItem{ID: "a", Index: 1, Status: domain.ItemCompleted})})
	s.applyEvent("j", domain.Event{ID: 12, ItemID: "b", Type: domain.EventItemFailed, Payload: mustJSON(domain.JobItem{ID: "b", Index: 2, Status: domain.ItemFailed})})
	j := s.Job("j")
	if j.CompletedItems != 1 || j.FailedItems != 1 || j.Done() != 1 {
		t.Fatalf("counters completed=%d failed=%d done=%d, want 1, 1, 1", j.CompletedItems, j.FailedItems, j.Done())
	}
}

// AVG SPEED was empty until the first track finished, because only finished
// tracks' file sizes counted: the bytes of tracks still downloading count too,
// and a finished track counts once, by its file size.
func TestAvgSpeedCountsTracksStillDownloading(t *testing.T) {
	started := at(0)
	j := &Job{Job: domain.Job{ID: "j", Status: domain.JobRunning, StartedAt: &started}}
	j.Items = []domain.JobItem{
		{ID: "a", Index: 1, Status: domain.ItemDownloading, Progress: domain.ItemProgress{Download: 0.5, DownloadBytes: 30 << 20}},
		{ID: "b", Index: 2, Status: domain.ItemDecrypting, Progress: domain.ItemProgress{Download: 1, DownloadBytes: 20 << 20}},
	}
	if got, want := j.AvgSpeed(started.Add(10*time.Second)), float64(5<<20); got != want {
		t.Fatalf("avg speed with nothing finished = %v, want %v", got, want)
	}
	// Track b finishes: its file size replaces its download bytes.
	j.Items[1] = domain.JobItem{ID: "b", Index: 2, Status: domain.ItemCompleted, FileSize: 21 << 20, Progress: domain.ItemProgress{Download: 1, DownloadBytes: 20 << 20}}
	if got, want := j.AvgSpeed(started.Add(10*time.Second)), float64(51<<20)/10; got != want {
		t.Fatalf("avg speed after one finished = %v, want %v", got, want)
	}
}

// A track working through the finish phase must not reset the time-left
// estimate: Left showed "—" for the whole of a job's second half because every
// finished track stepped the job's progress back and the estimate began again.
func TestRemainingSurvivesTracksFinishing(t *testing.T) {
	s, _ := newTestStore(t)
	now := at(0)
	s.SetClock(func() time.Time { return now })
	job := domain.Job{ID: "j", Status: domain.JobRunning, TotalItems: 2}
	s.ApplySnapshot(client.DownloadList{Downloads: []domain.Job{job}})
	got := domain.ItemProgress{Resolved: true, Download: 1, Decrypt: 1}
	remuxed := got
	remuxed.Remuxed = true
	verified := remuxed
	verified.Verified = true
	tagged := verified
	tagged.Tagged = true
	s.applyDetail("j", client.DownloadDetail{Job: job, EventCursor: 1, Items: []domain.JobItem{
		{ID: "a", Index: 1, Status: domain.ItemRemuxing, Progress: got},
		{ID: "b", Index: 2, Status: domain.ItemDownloading, Progress: domain.ItemProgress{Resolved: true}},
	}})
	finish := []domain.JobItem{
		{ID: "a", Index: 1, Status: domain.ItemSaving, Progress: remuxed},
		{ID: "a", Index: 1, Status: domain.ItemSaving, Progress: verified},
		{ID: "a", Index: 1, Status: domain.ItemTagging, Progress: verified},
		{ID: "a", Index: 1, Status: domain.ItemTagging, Progress: tagged},
		{ID: "a", Index: 1, Status: domain.ItemCompleted, Progress: tagged},
	}
	id := int64(1)
	send := func(it domain.JobItem) {
		id++
		s.applyEvent("j", domain.Event{ID: id, ItemID: it.ID, Type: domain.EventItemProgress, Payload: mustJSON(it)})
	}
	for step := 1; step <= 12; step++ {
		now = at(0).Add(time.Duration(step) * 2 * time.Second)
		send(domain.JobItem{ID: "b", Index: 2, Status: domain.ItemDownloading,
			Progress: domain.ItemProgress{Resolved: true, Download: float64(step) / 14}})
		if step <= len(finish) {
			send(finish[step-1])
		}
		if r := s.Job("j").Remaining; step >= 3 && r <= 0 {
			t.Fatalf("step %d: no time left estimate while progress rises (progress %.3f)", step, s.Job("j").Progress)
		}
	}
}
