package store

import (
	"encoding/json"
	"sort"
	"time"

	"amdl/internal/client"
	"amdl/internal/domain"
	"amdl/internal/progress"
)

// Job is one download as the app knows it.
type Job struct {
	domain.Job
	// Items are the job's tracks, by index, once the app has followed it;
	// nil for a job only the list feed has reported.
	Items []domain.JobItem
	Hooks []domain.HookState
	// Activity is what happened to the job, newest first, from its events.
	Activity []Activity
	// Stages is how far the job's tracks have come on average, and Progress
	// their weighted sum (progress.Weights).
	Stages   progress.Stages
	Progress float64
	// Remaining estimates the time left from the recent progress rate; 0
	// when there is not enough to go on.
	Remaining time.Duration

	rate           progress.Rate
	seq            int
	snapshotCursor int64
}

// Activity is one line of a job's activity log.
type Activity struct {
	At   time.Time
	Text string
	Kind ActivityKind
}

// ActivityKind colors an activity line.
type ActivityKind int

// Activity kinds.
const (
	ActivityInfo ActivityKind = iota
	ActivityDone
	ActivityWarn
	ActivityFailed
)

// maxActivity bounds a job's activity log.
const maxActivity = 60

// Name is the job's title, or its link before the input resolves.
func (j *Job) Name() string {
	if j.Title != "" {
		return j.Title
	}
	return j.Input
}

// By is the job's artist, curator or, failing both, its type.
func (j *Job) By() string {
	switch {
	case j.ArtistName != "":
		return j.ArtistName
	case j.CuratorName != "":
		return j.CuratorName
	}
	return j.Type
}

// Done counts the tracks that are finished, written now or found on disk.
func (j *Job) Done() int { return j.CompletedItems + j.SkippedItems }

// Album is the job's album bar, from the tracks the app has followed.
func (j *Job) Album() progress.Album { return progress.AlbumProgress(j.Status, j.Items) }

// Percent is the job's overall progress as its album bar states it, so every
// number the app shows for a job agrees with the web console's; a job whose
// tracks the app has not followed falls back to its counters.
func (j *Job) Percent() int {
	if len(j.Items) > 0 {
		return j.Album().Percent
	}
	return int(j.Progress*100 + 0.5)
}

// Bytes sums the file sizes of the tracks written so far in this run.
func (j *Job) Bytes() int64 {
	var n int64
	for _, it := range j.Items {
		if it.Status == domain.ItemCompleted {
			n += it.FileSize
		}
	}
	return n
}

// AvgSpeed is the bytes written per second of the run, until now or until
// it finished, from the sizes of finished tracks: the API reports no live
// transfer rate, so this is an average and moves only as tracks finish. 0
// when unknown.
func (j *Job) AvgSpeed(now time.Time) float64 {
	if j.StartedAt == nil {
		return 0
	}
	if j.FinishedAt != nil {
		now = *j.FinishedAt
	}
	elapsed := now.Sub(*j.StartedAt).Seconds()
	if elapsed < 1 {
		return 0
	}
	return float64(j.Bytes()) / elapsed
}

// Phase is what a job is doing, for its status pill.
type Phase int

// Job phases.
const (
	PhaseQueued Phase = iota
	PhaseResolving
	PhaseDownloading
	PhaseDecrypting
	PhaseFinishing
	PhaseDone
	PhaseFailed
	PhaseCancelled
)

// Phase reads the job's status and, while it runs, what most of its active
// tracks do.
func (j *Job) Phase() Phase {
	switch j.Status {
	case domain.JobQueued:
		return PhaseQueued
	case domain.JobCompleted:
		return PhaseDone
	case domain.JobFailed:
		return PhaseFailed
	case domain.JobCancelled:
		return PhaseCancelled
	}
	if j.TotalItems == 0 {
		return PhaseResolving
	}
	var down, dec, fin int
	for _, it := range j.Items {
		switch it.Status {
		case domain.ItemWaitingDownload, domain.ItemDownloading:
			down++
		case domain.ItemWaitingDecrypt, domain.ItemDecrypting:
			dec++
		case domain.ItemRemuxing, domain.ItemTagging, domain.ItemSaving:
			fin++
		}
	}
	switch {
	case down == 0 && dec == 0 && fin > 0:
		return PhaseFinishing
	case dec > down:
		return PhaseDecrypting
	}
	return PhaseDownloading
}

// recompute refreshes the track counters and the derived progress fields.
func (j *Job) recompute(now time.Time) {
	j.recount()
	j.Stages = progress.JobStages(j.Job, j.Items)
	j.Progress = j.Stages.Weighted()
	if j.Status == domain.JobRunning {
		j.rate.Add(now, j.Progress)
		j.Remaining = j.rate.Remaining()
	} else {
		j.rate, j.Remaining = progress.Rate{}, 0
	}
}

// recount fills the job's track counters from its tracks, as the backend
// does on every read: an item event carries the track, not the job, so the
// counters would otherwise keep the last job payload's values while the
// tracks move on. A partial track list leaves them alone.
func (j *Job) recount() {
	if len(j.Items) == 0 || len(j.Items) < j.TotalItems {
		return
	}
	j.CompletedItems, j.SkippedItems, j.FailedItems, j.CancelledItems = 0, 0, 0, 0
	for _, it := range j.Items {
		switch it.Status {
		case domain.ItemCompleted:
			j.CompletedItems++
		case domain.ItemSkipped:
			j.SkippedItems++
		case domain.ItemFailed:
			j.FailedItems++
		case domain.ItemCancelled:
			j.CancelledItems++
		}
	}
}

// ApplySnapshot replaces the job list with l, as the feed's first message
// does; tests use it to seed the store.
func (s *Store) ApplySnapshot(l client.DownloadList) {
	s.Loaded = true
	s.Counts = l.StatusCounts
	keep := map[string]*Job{}
	s.Jobs = s.Jobs[:0]
	for _, dj := range l.Downloads {
		j := s.byID[dj.ID]
		if j == nil {
			j = &Job{}
		}
		j.Job = dj
		j.seq = s.nextSeq
		s.nextSeq++
		j.recompute(s.now())
		keep[j.ID] = j
		s.Jobs = append(s.Jobs, j)
		if client.Terminal(j.Status) {
			s.notified[j.ID] = j.Status
		}
	}
	for id, cancel := range s.followers {
		if keep[id] == nil && id != s.DetailID {
			cancel()
			delete(s.followers, id)
		}
	}
	s.byID = keep
	if s.DetailID != "" && keep[s.DetailID] == nil {
		s.DetailID = ""
	}
	for id := range s.Selection {
		if keep[id] == nil {
			delete(s.Selection, id)
		}
	}
	s.sortJobs()
	for _, j := range s.Jobs {
		if j.Status == domain.JobRunning {
			s.follow(j, false)
		}
	}
}

func (s *Store) applyMessage(m domain.DownloadFeedMessage) {
	s.Counts = m.StatusCounts
	switch m.Type {
	case "download_upserted":
		if m.Job != nil {
			s.upsert(*m.Job)
		}
	case "download_deleted":
		s.remove(m.JobID)
	}
}

// upsert merges a job snapshot newer than what the store holds.
func (s *Store) upsert(dj domain.Job) *Job {
	j := s.byID[dj.ID]
	if j == nil {
		j = &Job{seq: s.nextSeq}
		s.nextSeq++
		s.byID[dj.ID] = j
		s.Jobs = append(s.Jobs, j)
		j.Job = dj
		s.sortJobs()
	} else {
		j.Job = dj
	}
	j.recompute(s.now())
	s.noteStatus(j)
	if j.Status == domain.JobRunning {
		s.follow(j, false)
	} else {
		s.unfollowUnlessNeeded(j.ID)
	}
	return j
}

// noteStatus tells the notifier about a run that just finished, once.
func (s *Store) noteStatus(j *Job) {
	prev, seen := s.notified[j.ID]
	if !client.Terminal(j.Status) {
		if seen {
			delete(s.notified, j.ID) // a retry starts a new run
		}
		return
	}
	if seen && prev == j.Status {
		return
	}
	s.notified[j.ID] = j.Status
	if s.notifier != nil && (j.Status == domain.JobCompleted || j.Status == domain.JobFailed) {
		s.notifier.JobFinished(j)
	}
}

func (s *Store) remove(id string) {
	j := s.byID[id]
	if j == nil {
		return
	}
	delete(s.byID, id)
	s.Search.gone[id] = true
	delete(s.Selection, id)
	delete(s.notified, id)
	if cancel := s.followers[id]; cancel != nil {
		cancel()
		delete(s.followers, id)
	}
	if s.DetailID == id {
		s.DetailID = ""
	}
	for i, x := range s.Jobs {
		if x == j {
			s.Jobs = append(s.Jobs[:i], s.Jobs[i+1:]...)
			break
		}
	}
}

// sortJobs orders the jobs newest first; jobs created together keep the
// order they were submitted in, as the backend lists them.
func (s *Store) sortJobs() {
	sort.SliceStable(s.Jobs, func(a, b int) bool {
		ja, jb := s.Jobs[a], s.Jobs[b]
		if !ja.CreatedAt.Equal(jb.CreatedAt) {
			return ja.CreatedAt.After(jb.CreatedAt)
		}
		return ja.seq < jb.seq
	})
}

func (s *Store) applyDetail(id string, d client.DownloadDetail) {
	j := s.byID[id]
	if j == nil {
		j = s.upsert(d.Job)
	} else {
		j.Job = d.Job
		s.noteStatus(j)
	}
	j.Items = append(j.Items[:0], d.Items...)
	sort.SliceStable(j.Items, func(a, b int) bool { return j.Items[a].Index < j.Items[b].Index })
	j.Hooks = d.Hooks
	j.snapshotCursor = d.EventCursor
	j.recompute(s.now())
}

func (s *Store) applyEvent(id string, e domain.Event) {
	j := s.byID[id]
	if j == nil {
		return
	}
	if text, kind, ok := describe(j, e); ok {
		j.Activity = append([]Activity{{At: e.CreatedAt, Text: text, Kind: kind}}, j.Activity...)
		if len(j.Activity) > maxActivity {
			j.Activity = j.Activity[:maxActivity]
		}
	}
	if e.ID <= j.snapshotCursor || len(e.Payload) == 0 {
		return // history older than the snapshot the store holds
	}
	if e.ItemID != "" {
		var it domain.JobItem
		if json.Unmarshal(e.Payload, &it) != nil || it.ID == "" {
			return
		}
		j.setItem(it)
		j.recompute(s.now())
		return
	}
	switch e.Type {
	case domain.EventJobQueued, domain.EventJobStarted, domain.EventJobRetried, domain.EventJobRecovered,
		domain.EventResolvedInput, domain.EventJobFinished, domain.EventJobFailed, domain.EventJobCancelled:
		var dj domain.Job
		if json.Unmarshal(e.Payload, &dj) == nil && dj.ID == id {
			j.Job = dj
			j.recompute(s.now())
			s.noteStatus(j)
		}
	}
}

func (j *Job) setItem(it domain.JobItem) {
	for i := range j.Items {
		if j.Items[i].ID == it.ID {
			j.Items[i] = it
			return
		}
	}
	j.Items = append(j.Items, it)
	sort.SliceStable(j.Items, func(a, b int) bool { return j.Items[a].Index < j.Items[b].Index })
}

// Tab is a filter of the queue.
type Tab int

// Queue tabs, in the order the queue shows them; the zero value, All, is
// the one it opens on.
const (
	TabAll Tab = iota
	TabActive
	TabDone
	TabFailed
)

// Matches reports whether j belongs on tab t.
func (t Tab) Matches(j *Job) bool {
	switch t {
	case TabActive:
		return j.Status == domain.JobQueued || j.Status == domain.JobRunning
	case TabDone:
		return j.Status == domain.JobCompleted
	case TabFailed:
		return j.Status == domain.JobFailed || j.Status == domain.JobCancelled
	}
	return true
}

// Count is the backend's count of the jobs on tab t.
func (s *Store) Count(t Tab) int {
	c := s.Counts
	switch t {
	case TabActive:
		return c.Queued + c.Running
	case TabDone:
		return c.Completed
	case TabFailed:
		return c.Failed + c.Cancelled
	}
	return c.Total
}

// Filter returns the jobs on tab t whose title, artist or link contains
// query (any case), newest first.
func (s *Store) Filter(t Tab, query string) []*Job {
	var out []*Job
	for _, j := range s.Jobs {
		if t.Matches(j) && matchQuery(j, query) {
			out = append(out, j)
		}
	}
	return out
}

// Now returns the job the "downloading now" card shows: the running job
// that started last, or nil.
func (s *Store) Now() *Job {
	var now *Job
	for _, j := range s.Jobs {
		if j.Status != domain.JobRunning {
			continue
		}
		if now == nil || startedAt(j).After(startedAt(now)) {
			now = j
		}
	}
	return now
}

func startedAt(j *Job) time.Time {
	if j.StartedAt != nil {
		return *j.StartedAt
	}
	return j.CreatedAt
}

// Summary is what the tray and the menu bar panel show.
type Summary struct {
	Running, Queued int
	// TracksDone and TracksTotal count the tracks of the running jobs.
	TracksDone, TracksTotal int
	// Progress is the weighted progress of the running jobs' tracks.
	Progress float64
	// FailedToday counts jobs that failed since local midnight.
	FailedToday int
	// CompletedToday counts jobs that completed since local midnight, and
	// TracksToday the tracks they wrote.
	CompletedToday, TracksToday int
	// Now is the job the "downloading now" card shows, or nil.
	Now *Job
}

// Summary sums up the jobs the store knows.
func (s *Store) Summary() Summary {
	var sum Summary
	now := s.now()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	weighted := 0.0
	for _, j := range s.Jobs {
		switch j.Status {
		case domain.JobRunning:
			sum.Running++
			sum.TracksDone += j.Done()
			sum.TracksTotal += j.TotalItems
			weighted += j.Progress * float64(max(j.TotalItems, 1))
		case domain.JobQueued:
			sum.Queued++
		case domain.JobFailed:
			if j.FinishedAt != nil && !j.FinishedAt.Before(midnight) {
				sum.FailedToday++
			}
		case domain.JobCompleted:
			if j.FinishedAt != nil && !j.FinishedAt.Before(midnight) {
				sum.CompletedToday++
				sum.TracksToday += j.CompletedItems
			}
		}
	}
	if sum.TracksTotal > 0 {
		sum.Progress = weighted / float64(sum.TracksTotal)
	}
	sum.Now = s.Now()
	return sum
}

// Select makes id the only selected job; with extend it toggles id instead.
func (s *Store) Select(id string, extend bool) {
	if !extend {
		clear(s.Selection)
		s.Selection[id] = true
		return
	}
	if s.Selection[id] {
		delete(s.Selection, id)
	} else {
		s.Selection[id] = true
	}
}

// ClearSelection selects nothing.
func (s *Store) ClearSelection() { clear(s.Selection) }

// Selected returns the selected jobs, newest first.
func (s *Store) Selected() []*Job {
	var out []*Job
	for _, j := range s.Jobs {
		if s.Selection[j.ID] {
			out = append(out, j)
		}
	}
	return out
}
