// Package store holds the desktop app's state: the job list as the backend
// reports it, the tracks of the jobs being followed, the connection, the
// selection and the link field. Every field is read and changed on the UI
// thread; background work hands its results over through the post function
// the store was made with, which runs them there and redraws.
package store

import (
	"context"
	"errors"
	"time"

	"amdl/internal/client"
	"amdl/internal/domain"
)

// Notifier hears about jobs that finished while the app watched.
type Notifier interface {
	// JobFinished is called once per run of a job that reached completed or
	// failed after the app saw it unfinished.
	JobFinished(j *Job)
}

// Store is the app's state. Use it only on the UI thread.
type Store struct {
	post     func(func())
	notifier Notifier
	now      func() time.Time

	client *client.Client
	ctx    context.Context
	cancel context.CancelFunc

	// Conn is the job feed's connection, and ConnChanged when it last
	// changed phase.
	Conn        client.ConnState
	ConnChanged time.Time
	// LastLive is when the feed was last known to be live: the age of what
	// an offline view shows.
	LastLive time.Time
	// Loaded is set once the first snapshot arrived.
	Loaded bool

	// Jobs are newest first, as the backend lists them.
	Jobs []*Job
	byID map[string]*Job
	// Counts are the backend's status counts over all jobs, which can be
	// more than Jobs holds.
	Counts domain.JobStatusCounts

	// Selection holds the ids of the selected jobs.
	Selection map[string]bool
	// DetailID is the job the detail screen shows, or "".
	DetailID string

	// Omni is home's link and search field.
	Omni Omnibox
	// Search is the catalog search for the words in Omni.
	Search Search
	// Notice is the latest message for a toast, or zero.
	Notice Notice
	// HookTests are the webhooks' last tests, by name.
	HookTests map[string]HookTest
	// Server is the backend's health, resources and settings in effect, for
	// Settings and the tray panel.
	Server Server
	// Config is the backend's runtime configuration, which Settings edits.
	Config Config
	// Login is the Apple Music sign-in Settings runs.
	Login Login

	nextSeq   int
	followers map[string]context.CancelFunc
	notified  map[string]domain.JobStatus
	// tracksRead names the jobs whose tracks WantTracks read.
	tracksRead map[string]bool
}

// Notice is a short message about an action's outcome.
type Notice struct {
	Text  string
	Error bool
	At    time.Time
}

// New returns an empty store. post runs a function on the UI thread and
// redraws; notifier may be nil.
func New(post func(func()), notifier Notifier) *Store {
	s := &Store{post: post, notifier: notifier, now: time.Now}
	s.reset()
	return s
}

func (s *Store) reset() {
	s.Conn, s.ConnChanged, s.LastLive, s.Loaded = client.ConnState{Phase: client.Connecting}, s.now(), time.Time{}, false
	s.Jobs, s.byID, s.Counts = nil, map[string]*Job{}, domain.JobStatusCounts{}
	s.Selection, s.DetailID = map[string]bool{}, ""
	s.followers, s.notified, s.tracksRead = map[string]context.CancelFunc{}, map[string]domain.JobStatus{}, map[string]bool{}
	s.Omni.reset()
	s.Search.reset()
	s.Server, s.Config, s.Login = Server{}, Config{}, Login{}
}

// SetClock replaces the clock the store stamps requests with, which a test
// of a screen sets to its tester's, so that "how long has it waited" means
// the same to both.
func (s *Store) SetClock(now func() time.Time) { s.now = now }

// SetNotifier sets who hears about finished jobs.
func (s *Store) SetNotifier(n Notifier) { s.notifier = n }

// Client returns the backend the store follows, or nil.
func (s *Store) Client() *client.Client { return s.client }

// Connect starts following c, dropping everything known about the previous
// backend. A nil c only disconnects.
func (s *Store) Connect(c *client.Client) {
	if s.cancel != nil {
		s.cancel()
	}
	s.reset()
	s.client = c
	if c == nil {
		s.ctx, s.cancel = nil, nil
		return
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	ctx := s.ctx
	go c.FollowFeed(ctx, client.FeedHandlers{
		Snapshot: func(l client.DownloadList) { s.postIf(ctx, func() { s.ApplySnapshot(l) }) },
		Message:  func(m domain.DownloadFeedMessage) { s.postIf(ctx, func() { s.applyMessage(m) }) },
		State:    func(st client.ConnState) { s.postIf(ctx, func() { s.setConn(st) }) },
	})
}

// Close stops all background work.
func (s *Store) Close() { s.Connect(nil) }

// postIf posts fn unless ctx, the connection it belongs to, has ended by
// the time it runs: results of a backend the store left are dropped.
func (s *Store) postIf(ctx context.Context, fn func()) {
	s.post(func() {
		if ctx.Err() == nil {
			fn()
		}
	})
}

func (s *Store) setConn(st client.ConnState) {
	if st.Phase != s.Conn.Phase {
		s.ConnChanged = s.now()
	}
	if s.Conn.Phase == client.Live || st.Phase == client.Live {
		s.LastLive = s.now()
	}
	s.Conn = st
}

// Job returns the job with id, or nil.
func (s *Store) Job(id string) *Job { return s.byID[id] }

// Detail returns the job the detail screen shows, or nil.
func (s *Store) Detail() *Job { return s.byID[s.DetailID] }

// OpenDetail shows job id in the detail screen and follows its events from
// the start, for the activity log.
func (s *Store) OpenDetail(id string) {
	if s.DetailID == id {
		return
	}
	prev := s.DetailID
	s.DetailID = id
	if prev != "" {
		s.unfollowUnlessNeeded(prev)
	}
	if j := s.byID[id]; j != nil {
		s.follow(j, true)
	}
}

// CloseDetail leaves the detail screen.
func (s *Store) CloseDetail() {
	prev := s.DetailID
	s.DetailID = ""
	if prev != "" {
		s.unfollowUnlessNeeded(prev)
	}
}

// follow starts a follower for j unless one runs. history follows its events
// from the first one, so the activity log shows the whole run.
func (s *Store) follow(j *Job, history bool) {
	if s.client == nil || s.followers[j.ID] != nil {
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.followers[j.ID] = cancel
	id, c := j.ID, s.client
	go func() {
		err := c.FollowJob(ctx, id, client.JobHandlers{
			FromStart: history,
			Detail:    func(d client.DownloadDetail) { s.postIf(ctx, func() { s.applyDetail(id, d) }) },
			Event:     func(e domain.Event) { s.postIf(ctx, func() { s.applyEvent(id, e) }) },
		})
		s.postIf(ctx, func() {
			delete(s.followers, id)
			if errors.Is(err, client.ErrJobGone) {
				s.remove(id)
				return
			}
			// The job may have been retried while its old run wound down.
			if j := s.byID[id]; j != nil && j.Status == domain.JobRunning {
				s.follow(j, false)
			}
		})
	}()
}

func (s *Store) unfollowUnlessNeeded(id string) {
	j := s.byID[id]
	if j != nil && (j.Status == domain.JobRunning || id == s.DetailID) {
		return
	}
	if cancel := s.followers[id]; cancel != nil {
		cancel()
		delete(s.followers, id)
	}
}

// Following reports whether the store follows job id's events.
func (s *Store) Following(id string) bool { return s.followers[id] != nil }

// Cancel asks the backend to cancel job id.
func (s *Store) Cancel(id string) { s.act(id, "Cancelled", (*client.Client).Cancel) }

// Delete asks the backend to delete finished job id; the feed then drops
// it.
func (s *Store) Delete(id string) { s.act(id, "Deleted", (*client.Client).Delete) }

// Retry asks the backend to retry job id.
func (s *Store) Retry(id string) { s.act(id, "Queued again", (*client.Client).Retry) }

func (s *Store) act(id, okText string, call func(*client.Client, context.Context, string) error) {
	c, ctx := s.client, s.ctx
	if c == nil {
		return
	}
	go func() {
		err := call(c, ctx, id)
		s.postIf(ctx, func() {
			if err != nil {
				s.setNotice(err.Error(), true)
			} else {
				s.setNotice(okText, false)
			}
		})
	}()
}

func (s *Store) setNotice(text string, isErr bool) {
	s.Notice = Notice{Text: text, Error: isErr, At: s.now()}
}
