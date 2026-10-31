// Package demo is an amdl backend held in memory, for the desktop app's demo
// that the browser build runs: a made-up catalog, jobs whose tracks move,
// and writes that change only memory. It answers the API routes the app
// calls (internal/api/openapi.yaml) with the client and domain types,
// through an http.RoundTripper, so no port opens and nothing leaves the
// process.
package demo

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"amdl/internal/config"
	"amdl/internal/domain"
	"amdl/internal/progress"
)

// Address is the demo backend's address, which clients of it name.
const Address = "http://demo"

// Tick is how often a running backend moves its jobs on.
const Tick = 500 * time.Millisecond

const (
	// feedEvery is how often the feed sends a running job's progress, as
	// the real feed's ticker does.
	feedEvery = 2 * time.Second
	// maxRunning jobs run at once, each with up to perJob tracks under way.
	maxRunning = 2
	perJob     = 2
	// maxEvents bounds the events a job keeps for its stream to replay.
	maxEvents = 4000
)

// Backend is the demo's backend. Its methods are safe for concurrent use.
type Backend struct {
	now func() time.Time
	rt  http.RoundTripper

	mu        sync.Mutex
	seq       int64
	jobs      []*job // newest first
	nextJob   int
	config    map[string]any
	signedIn  bool
	sinceFeed time.Duration
	feeds     map[chan domain.DownloadFeedMessage]bool
	covers    map[string][]byte
}

type job struct {
	domain.Job
	album  *album
	items  []domain.JobItem
	events []domain.Event
	subs   map[chan domain.Event]bool
	// ran is how long each track has been under way, by index.
	ran []time.Duration
	// sent is the progress the feed last carried.
	sent float64
}

// New returns a backend whose clock is now, with its jobs as they stand at
// that moment: two running, one waiting, some finished, one failed.
func New(now func() time.Time) *Backend {
	b := &Backend{now: now, signedIn: true, feeds: map[chan domain.DownloadFeedMessage]bool{}, covers: map[string][]byte{}}
	view, err := config.MutableView(config.Default())
	if err != nil {
		panic("demo: the default config does not render: " + err.Error())
	}
	b.config = view
	b.rt = roundTripper{b.handler()}
	if dl, ok := view["download"].(map[string]any); ok {
		dl["downloads_dir"] = musicDir
	}
	t := now()
	seed := []struct {
		album  *album
		status domain.JobStatus
		ago    time.Duration
		done   int
	}{
		{albums[6], domain.JobQueued, 20 * time.Second, 0},
		{albums[2], domain.JobRunning, 40 * time.Second, 1},
		{albums[0], domain.JobRunning, 3 * time.Minute, 5},
		{albums[4], domain.JobCompleted, 26 * time.Hour, -1},
		{albums[1], domain.JobFailed, 2 * 24 * time.Hour, 8},
		{albums[7], domain.JobCompleted, 4 * 24 * time.Hour, -1},
		{albums[5], domain.JobCancelled, 5 * 24 * time.Hour, 2},
	}
	for i := len(seed) - 1; i >= 0; i-- {
		s := seed[i]
		j := b.newJob(s.album, nil, t.Add(-s.ago))
		b.settle(j, s.status, s.done, t.Add(-s.ago))
	}
	return b
}

// Start moves the jobs on every Tick until ctx ends.
func (b *Backend) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(Tick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				b.Step(Tick)
			}
		}
	}()
}

// Transport is the RoundTripper that serves this backend to a client.
func (b *Backend) Transport() http.RoundTripper { return b.rt }

// musicDir is the downloads folder the demo reports, a NAS's.
const musicDir = "/volume1/music"

// newJob adds a queued job for a, or for one of its songs, created at t.
func (b *Backend) newJob(a *album, one *song, t time.Time) *job {
	b.nextJob++
	j := &job{album: a, subs: map[chan domain.Event]bool{}}
	j.Job = domain.Job{
		ID: fmt.Sprintf("demo-%04d", b.nextJob), Input: a.url(), Type: "album", Storefront: "us",
		Title: a.title, ArtworkURL: a.artwork(), ArtistName: a.artist.name, ArtistURL: a.artist.url(),
		ReleaseDate: a.releaseDate(), Genre: a.artist.genre, ArtworkBgColor: a.bg, ArtworkTextColor1: a.fg,
		ArtworkTextColor2: "ffffff", Status: domain.JobQueued, CreatedAt: t, UpdatedAt: t,
	}
	indexes := make([]int, len(a.tracks))
	for i := range indexes {
		indexes[i] = i
	}
	if one != nil {
		j.Input, j.Type, j.Title, indexes = one.url(), "song", a.tracks[one.index], []int{one.index}
	}
	for n, i := range indexes {
		id := songID(a, i)
		j.items = append(j.items, domain.JobItem{
			ID: fmt.Sprintf("%s-%02d", j.ID, n+1), JobID: j.ID, AdamID: id, Kind: "song", Index: n + 1,
			Title: a.tracks[i], Artist: a.artist.name, Album: a.title, DurationMS: durationMS(id),
			ArtworkURL: a.artwork(), HasLyrics: hash(id)%3 != 0, Status: domain.ItemQueued,
			Progress: domain.ItemProgress{Resolved: true}, CreatedAt: t, UpdatedAt: t,
		})
	}
	j.TotalItems, j.ran = len(j.items), make([]time.Duration, len(j.items))
	b.jobs = append([]*job{j}, b.jobs...)
	b.emit(j, domain.EventJobQueued, "", j.snapshot(), t)
	return j
}

// settle puts a seeded job where it stands: done tracks finished, and for
// a running job the next ones part way.
func (b *Backend) settle(j *job, status domain.JobStatus, done int, t time.Time) {
	if status == domain.JobQueued {
		return
	}
	if done < 0 {
		done = len(j.items)
	}
	j.Status, j.StartedAt = domain.JobRunning, ptr(t.Add(2*time.Second))
	for i := range j.items {
		switch {
		case i < done:
			j.ran[i] = j.length(i)
			j.place(i, t)
		case status == domain.JobRunning && i < done+perJob:
			j.ran[i] = j.length(i) * time.Duration(1+i%3) / 4
			j.place(i, t)
		case status == domain.JobFailed:
			it := &j.items[i]
			it.Status, it.Error = domain.ItemFailed, "the song is not available in the us storefront"
		case status == domain.JobCancelled:
			j.items[i].Status = domain.ItemCancelled
		}
	}
	if status != domain.JobRunning {
		j.Status, j.FinishedAt = status, ptr(t.Add(time.Duration(len(j.items))*40*time.Second))
		if status == domain.JobCancelled {
			j.Error = "cancelled"
		}
	}
	j.sent = j.progress()
}

// Step moves the jobs on by dt: waiting jobs start while fewer than
// maxRunning run, and each running job's tracks go through their stages.
func (b *Backend) Step(dt time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.startWaiting(now)
	for _, j := range b.jobs {
		if j.Status == domain.JobRunning {
			b.advance(j, dt, now)
		}
	}
	b.sinceFeed += dt
	if b.sinceFeed >= feedEvery {
		b.sinceFeed = 0
		for _, j := range b.jobs {
			if p := j.progress(); j.Status == domain.JobRunning && p != j.sent {
				j.sent = p
				b.feed(j)
			}
		}
	}
	b.startWaiting(now)
}

func (b *Backend) startWaiting(now time.Time) {
	running := 0
	var waiting []*job
	for _, j := range b.jobs {
		switch j.Status {
		case domain.JobRunning:
			running++
		case domain.JobQueued:
			waiting = append(waiting, j)
		}
	}
	// The oldest waiting job goes first.
	for i := len(waiting) - 1; i >= 0 && running < maxRunning; i-- {
		j := waiting[i]
		j.Status, j.StartedAt, j.UpdatedAt = domain.JobRunning, ptr(now), now
		b.emit(j, domain.EventJobStarted, "", j.snapshot(), now)
		b.feed(j)
		running++
	}
}

func (b *Backend) advance(j *job, dt time.Duration, now time.Time) {
	active := 0
	for i := range j.items {
		it := &j.items[i]
		if finished(it.Status) {
			continue
		}
		if it.Status == domain.ItemQueued && active >= perJob {
			continue
		}
		active++
		j.ran[i] += dt
		j.place(i, now)
		if it.Status == domain.ItemCompleted {
			b.emit(j, domain.EventItemCompleted, it.ID, *it, now)
			j.sent = j.progress()
			b.feed(j)
			continue
		}
		b.emit(j, domain.EventItemProgress, it.ID, *it, now)
	}
	for _, it := range j.items {
		if !finished(it.Status) {
			return
		}
	}
	j.Status, j.FinishedAt, j.UpdatedAt = domain.JobCompleted, ptr(now), now
	for _, it := range j.items {
		if it.Status == domain.ItemFailed {
			j.Status = domain.JobFailed
		}
	}
	b.emit(j, domain.EventJobFinished, "", j.snapshot(), now)
	b.feed(j)
}

// length is how long track i takes in all: a download of 7 to 13 seconds,
// then the decryption and the writing.
func (j *job) length(i int) time.Duration {
	return downloadTime(j.items[i].AdamID) + decryptTime + writeTime
}

const (
	decryptTime = 2 * time.Second
	writeTime   = 1200 * time.Millisecond
)

func downloadTime(id string) time.Duration {
	return 7*time.Second + time.Duration(hash(id)%6000)*time.Millisecond
}

// place sets track i's stage and progress from how long it has run.
func (j *job) place(i int, now time.Time) {
	it := &j.items[i]
	dl := downloadTime(it.AdamID)
	t := j.ran[i]
	p := domain.ItemProgress{Resolved: true}
	switch {
	case t < dl:
		it.Status, p.Download = domain.ItemDownloading, float64(t)/float64(dl)
	case t < dl+decryptTime:
		it.Status, p.Download, p.Decrypt = domain.ItemDecrypting, 1, float64(t-dl)/float64(decryptTime)
	case t < dl+decryptTime+writeTime:
		p.CompleteTransfers()
		w := t - dl - decryptTime
		p.Remuxed, p.Verified = true, true
		switch {
		case w < writeTime/3:
			it.Status = domain.ItemRemuxing
		case w < writeTime*2/3:
			it.Status, p.Tagged = domain.ItemTagging, true
		default:
			it.Status, p.Tagged = domain.ItemSaving, true
		}
	default:
		p.CompleteTransfers()
		p.Remuxed, p.Verified, p.Tagged, p.Saved = true, true, true, true
		it.Status = domain.ItemCompleted
		it.FileSize = int64(durationMS(it.AdamID)) * 290
		it.OutputPath = fmt.Sprintf("%s/%s/%s/%02d %s.m4a", musicDir, j.album.artist.name, j.album.title, it.Index, it.Title)
		if it.HasLyrics {
			it.LyricsStatus = domain.LyricsFetched
		}
	}
	// The codec is chosen before the download starts.
	it.Codec, it.BitDepth, it.SampleRate = "alac", 24, 48000
	it.Progress, it.UpdatedAt = p, now
}

func finished(s domain.ItemStatus) bool {
	switch s {
	case domain.ItemCompleted, domain.ItemSkipped, domain.ItemFailed, domain.ItemCancelled:
		return true
	}
	return false
}

// snapshot is the job as the API answers it: counters from the tracks and
// the overall progress.
func (j *job) snapshot() domain.Job {
	s := j.Job
	s.CompletedItems, s.SkippedItems, s.FailedItems, s.CancelledItems = 0, 0, 0, 0
	for _, it := range j.items {
		switch it.Status {
		case domain.ItemCompleted:
			s.CompletedItems++
		case domain.ItemSkipped:
			s.SkippedItems++
		case domain.ItemFailed:
			s.FailedItems++
		case domain.ItemCancelled:
			s.CancelledItems++
		}
	}
	p := j.progress()
	s.Progress = &p
	return s
}

func (j *job) progress() float64 { return progress.Overall(j.Status, j.items) }

// emit records an event of j and hands it to j's streams.
func (b *Backend) emit(j *job, t domain.EventType, itemID string, payload any, now time.Time) {
	b.seq++
	ev := domain.Event{ID: b.seq, JobID: j.ID, ItemID: itemID, Type: t, Payload: domain.MarshalEventPayload(payload, nil), CreatedAt: now}
	j.events = append(j.events, ev)
	if len(j.events) > maxEvents {
		j.events = j.events[len(j.events)-maxEvents:]
	}
	for ch := range j.subs {
		select {
		case ch <- ev:
		default:
			// A stream that fell this far behind reads the job again when
			// it reconnects.
		}
	}
}

// feed tells the overview feeds about j.
func (b *Backend) feed(j *job) {
	snap := j.snapshot()
	msg := domain.DownloadFeedMessage{Type: "download_upserted", Job: &snap, EventID: b.seq, StatusCounts: b.counts()}
	for ch := range b.feeds {
		select {
		case ch <- msg:
		default:
		}
	}
}

func (b *Backend) counts() domain.JobStatusCounts {
	var c domain.JobStatusCounts
	for _, j := range b.jobs {
		switch j.Status {
		case domain.JobQueued:
			c.Queued++
		case domain.JobRunning:
			c.Running++
		case domain.JobCompleted:
			c.Completed++
		case domain.JobFailed:
			c.Failed++
		case domain.JobCancelled:
			c.Cancelled++
		}
		c.Total++
	}
	return c
}

func (b *Backend) job(id string) *job {
	for _, j := range b.jobs {
		if j.ID == id {
			return j
		}
	}
	return nil
}

// active is the queued or running job of input, or nil.
func (b *Backend) active(input string) *job {
	for _, j := range b.jobs {
		if j.Input == input && !j.Status.IsTerminal() {
			return j
		}
	}
	return nil
}

// cancel stops j: its unfinished tracks are cancelled.
func (b *Backend) cancel(j *job, now time.Time) {
	for i := range j.items {
		if !finished(j.items[i].Status) {
			j.items[i].Status, j.items[i].UpdatedAt = domain.ItemCancelled, now
		}
	}
	j.Status, j.Error, j.FinishedAt, j.UpdatedAt = domain.JobCancelled, "cancelled", ptr(now), now
	b.emit(j, domain.EventJobCancelled, "", j.snapshot(), now)
	b.feed(j)
}

// retry queues j again; its finished tracks stay done.
func (b *Backend) retry(j *job, now time.Time) {
	for i := range j.items {
		it := &j.items[i]
		if it.Status == domain.ItemCompleted || it.Status == domain.ItemSkipped {
			continue
		}
		it.ResetForRetry()
		j.ran[i] = 0
	}
	j.Status, j.Error, j.StartedAt, j.FinishedAt, j.UpdatedAt = domain.JobQueued, "", nil, nil, now
	// The demo's catalog serves every song a second time round.
	b.emit(j, domain.EventJobRetried, "", j.snapshot(), now)
	b.feed(j)
}

// priorJob is the newest job holding the album or song id, for a search
// hit's library mark.
func (b *Backend) priorJob(id string) (*job, bool) {
	for _, j := range b.jobs {
		if j.Status == domain.JobFailed || j.Status == domain.JobCancelled {
			continue
		}
		if j.album.id == id && j.Type == "album" {
			return j, true
		}
		for _, it := range j.items {
			if it.AdamID == id && (j.Type == "song" || j.Status == domain.JobCompleted) {
				return j, true
			}
		}
	}
	return nil, false
}

// sortedJobs is the jobs newest first, as GET /downloads lists them.
func (b *Backend) sortedJobs() []*job {
	out := append([]*job(nil), b.jobs...)
	sort.SliceStable(out, func(x, y int) bool { return out[x].CreatedAt.After(out[y].CreatedAt) })
	return out
}

func ptr[T any](v T) *T { return &v }
