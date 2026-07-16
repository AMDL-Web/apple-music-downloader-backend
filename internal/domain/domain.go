package domain

import (
	"encoding/json"
	"time"

	"amdl/internal/config"
)

type JobStatus string

const (
	JobQueued    JobStatus = "queued"
	JobRunning   JobStatus = "running"
	JobCompleted JobStatus = "completed"
	JobFailed    JobStatus = "failed"
	JobCancelled JobStatus = "cancelled"
)

// IsTerminal reports whether a job in this status will never emit another
// event: no worker is running and none will be scheduled.
func (s JobStatus) IsTerminal() bool {
	switch s {
	case JobCompleted, JobFailed, JobCancelled:
		return true
	default:
		return false
	}
}

type ItemStatus string

const (
	ItemQueued      ItemStatus = "queued"
	ItemResolving   ItemStatus = "resolving"
	ItemDownloading ItemStatus = "downloading"
	ItemDecrypting  ItemStatus = "decrypting"
	ItemRemuxing    ItemStatus = "remuxing"
	ItemTagging     ItemStatus = "tagging"
	ItemSaving      ItemStatus = "saving"
	ItemCompleted   ItemStatus = "completed"
	ItemFailed      ItemStatus = "failed"
	ItemSkipped     ItemStatus = "skipped_existing"
	ItemCancelled   ItemStatus = "cancelled"
)

// LyricsStatus records the durable outcome of one download attempt's lyrics
// fetch, complementing JobItem.HasLyrics: HasLyrics is the catalog's claim
// that lyrics exist, LyricsStatus is what actually happened when the backend
// tried to get them. Cleared on retry (see ResetForRetry) because the next
// attempt may succeed where this one failed. On finished items the value
// intentionally keeps describing the download that produced the file, even
// if a later re-resolve refreshes HasLyrics — the skew is meaningful (e.g.
// HasLyrics=true with LyricsNone: the file predates lyrics availability).
type LyricsStatus string

const (
	// LyricsPending: not determined yet — the item hasn't reached the lyrics
	// phase of a download, or predates this field.
	LyricsPending LyricsStatus = ""
	// LyricsFetched: lyrics were fetched and converted successfully; the
	// download that follows embeds and/or saves them per the config. The
	// flag reflects the fetch outcome only — whether the file itself was
	// produced is the item's own status (an item that fails later keeps
	// lyrics_status=fetched).
	LyricsFetched LyricsStatus = "fetched"
	// LyricsFailed: the catalog reported lyrics but fetching or converting
	// them failed; the download continued without lyrics.
	LyricsFailed LyricsStatus = "failed"
	// LyricsNone: the catalog reports no lyrics for this track.
	LyricsNone LyricsStatus = "none"
	// LyricsDisabled: lyrics exist but neither embed_lyrics nor
	// save_lyrics_file is enabled, so no fetch was attempted.
	LyricsDisabled LyricsStatus = "disabled"
)

type Job struct {
	ID           string `json:"id"`
	Input        string `json:"input"`
	Type         string `json:"type"`
	Storefront   string `json:"storefront,omitempty"`
	Title        string `json:"title,omitempty"`
	ArtworkURL   string `json:"artwork_url,omitempty"`
	CanonicalKey string `json:"-"`
	Force        bool   `json:"force"`
	// Overrides is the per-request job config overlay attached at submission;
	// nil for jobs submitted without one. It is persisted with the job and
	// applied on top of the live runtime config each time the job runs
	// (including retries and post-restart requeues). Credential fields are
	// redacted from its public JSON representation.
	Overrides   *config.DownloadOverrides `json:"overrides,omitempty"`
	Status      JobStatus                 `json:"status"`
	TotalItems  int                       `json:"total_items"`
	DoneItems   int                       `json:"done_items"`
	FailedItems int                       `json:"failed_items"`
	Error       string                    `json:"error,omitempty"`
	CreatedAt   time.Time                 `json:"created_at"`
	UpdatedAt   time.Time                 `json:"updated_at"`
}

// MarshalJSON is the public job representation used by create/list/detail and
// live feeds. Per-job media-user-tokens are credentials: keep them available
// in the internal/persisted Job for retry and recovery, but never echo them to
// API clients. A token-only override collapses to nil and is omitted entirely.
func (j Job) MarshalJSON() ([]byte, error) {
	type publicJob Job
	public := publicJob(j)
	public.Overrides = j.Overrides.WithoutMediaUserToken()
	return json.Marshal(public)
}

type JobItem struct {
	ID            string       `json:"id"`
	JobID         string       `json:"job_id"`
	AdamID        string       `json:"adam_id"`
	Kind          string       `json:"kind"`
	Index         int          `json:"index"`
	Title         string       `json:"title,omitempty"`
	Artist        string       `json:"artist,omitempty"`
	Album         string       `json:"album,omitempty"`
	ArtworkURL    string       `json:"artwork_url,omitempty"`
	HasLyrics     bool         `json:"has_lyrics"`
	LyricsStatus  LyricsStatus `json:"lyrics_status,omitempty"`
	Status        ItemStatus   `json:"status"`
	Progress      float64      `json:"progress"`
	Codec         string       `json:"codec,omitempty"`
	BitDepth      int          `json:"bit_depth,omitempty"`
	SampleRate    int          `json:"sample_rate,omitempty"`
	Bitrate       int          `json:"bitrate,omitempty"`
	RetryKind     string       `json:"retry_kind,omitempty"`
	Attempt       int          `json:"attempt,omitempty"`
	MaxAttempts   int          `json:"max_attempts,omitempty"`
	StatusMessage string       `json:"status_message,omitempty"`
	OutputPath    string       `json:"-"`
	Error         string       `json:"error,omitempty"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
}

// Finished reports whether the item ended in a state that a job retry must
// preserve: the track is already on disk (completed) or was intentionally left
// alone (skipped_existing). Everything else is re-processed by a retry.
func (i JobItem) Finished() bool {
	return i.Status == ItemCompleted || i.Status == ItemSkipped
}

// ResetForRetry returns the item to its pre-download queued state, clearing
// progress, quality, retry bookkeeping and error fields while keeping its
// identity (ID/JobID/AdamID/Index) and previously resolved metadata, so a
// retried job re-processes the track under the same item id.
func (i *JobItem) ResetForRetry() {
	i.Status = ItemQueued
	i.Progress = 0
	i.Codec = ""
	i.BitDepth, i.SampleRate, i.Bitrate = 0, 0, 0
	i.LyricsStatus = LyricsPending
	i.RetryKind = ""
	i.Attempt = 0
	i.MaxAttempts = 0
	i.StatusMessage = ""
	i.Error = ""
}

// CountItemProgress reports how many items in the slice are finished (completed
// or skipped) versus failed, using the same done/failed accounting applied when
// a job's DoneItems/FailedItems counters are refreshed. Deriving the counters
// from the live item list keeps a job's reported progress consistent with the
// items returned alongside it, even before the job reaches a terminal status.
func CountItemProgress(items []JobItem) (done, failed int) {
	for _, item := range items {
		switch item.Status {
		case ItemFailed:
			failed++
		case ItemCompleted, ItemSkipped:
			done++
		}
	}
	return done, failed
}

type Event struct {
	ID        int64     `json:"id"`
	JobID     string    `json:"job_id"`
	ItemID    string    `json:"item_id,omitempty"`
	Type      string    `json:"type"`
	Phase     string    `json:"phase,omitempty"`
	Message   string    `json:"message,omitempty"`
	Payload   string    `json:"payload,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// HookState is the snapshot-shaped view of one post-download hook's latest
// known status, derived from the hook_started/hook_succeeded/hook_failed
// events the dispatcher records. It exists so GET /downloads/{id} conveys
// the same information the SSE/WS event stream pushes incrementally — the
// snapshot and the stream are two access modes of one state, and a client
// that never subscribes must not be blind to hook outcomes.
type HookState struct {
	Name   string `json:"name"`
	Status string `json:"status"` // running | succeeded | failed | interrupted
	Error  string `json:"error,omitempty"`
}

// SummarizeHooks folds a job's hook events (ordered by id) into one HookState
// per hook name, keeping each hook's latest event as its status. stillRunning
// is the dispatcher's live in-flight signal for the job: a hook whose last
// recorded event is hook_started but with no execution in flight anymore
// (e.g. the process restarted mid-hook, so its terminal event will never
// arrive) is reported as "interrupted" rather than left "running" forever.
func SummarizeHooks(events []Event, stillRunning bool) []HookState {
	var order []string
	latest := map[string]Event{}
	for _, ev := range events {
		switch ev.Type {
		case "hook_started", "hook_succeeded", "hook_failed":
		default:
			continue
		}
		if _, seen := latest[ev.Phase]; !seen {
			order = append(order, ev.Phase)
		}
		latest[ev.Phase] = ev
	}
	out := make([]HookState, 0, len(order))
	for _, name := range order {
		ev := latest[name]
		state := HookState{Name: name}
		switch ev.Type {
		case "hook_succeeded":
			state.Status = "succeeded"
		case "hook_failed":
			state.Status = "failed"
			state.Error = ev.Message
		default: // hook_started
			if stillRunning {
				state.Status = "running"
			} else {
				state.Status = "interrupted"
			}
		}
		out = append(out, state)
	}
	return out
}

// EventDeleted is the tombstone event the manager records and broadcasts when a
// job is deleted. DeleteJob removes the job row and its old per-job events, then
// persists this global event so the overview feed can replay deletions from a
// snapshot cursor even if a client misses the live broadcast.
const EventDeleted = "job_deleted"

// PersistedOverviewMilestones are the persisted event types that change how a
// job appears in the GET /downloads list — its status, resolved
// title/total_items, or done/failed progress counters. The overview feed
// reacts only to these (plus the unpersisted EventDeleted) and ignores the
// higher-frequency per-item detail events (item_progress, codec_selected,
// retries, …) that don't alter the list-level view.
//
// This is the single source of truth for non-deletion overview milestones: the
// DB query that replays a cursor appends EventDeleted to this slice, and
// IsOverviewMilestone tests against the same combined set, so the two can never
// drift.
var PersistedOverviewMilestones = []string{
	"job_queued",
	"job_recovered",
	"job_retried",
	"job_started",
	"resolved_input", // title/total_items are populated by now
	"item_completed",
	"item_skipped",
	"item_failed",
	"job_finished",
	"job_failed",
	"job_cancelled",
}

// overviewMilestones is the set membership form of PersistedOverviewMilestones
// plus EventDeleted, for O(1) live-event filtering.
var overviewMilestones = func() map[string]struct{} {
	m := map[string]struct{}{EventDeleted: {}}
	for _, t := range PersistedOverviewMilestones {
		m[t] = struct{}{}
	}
	return m
}()

// IsOverviewMilestone reports whether an event of this type should wake the
// GET /downloads overview feed. Used to decide which live events reach
// overview subscribers at all.
func IsOverviewMilestone(eventType string) bool {
	_, ok := overviewMilestones[eventType]
	return ok
}

// DownloadFeedMessage is one push on the overview (GET /downloads) SSE/WS
// feed. Type is download_upserted (Job carries the affected job's latest
// snapshot, with live-derived progress counters) or download_deleted (only
// JobID is set). EventID is the persisted-event cursor a client hands back to
// resume.
type DownloadFeedMessage struct {
	Type    string `json:"type"`
	Job     *Job   `json:"job,omitempty"`
	JobID   string `json:"job_id,omitempty"`
	EventID int64  `json:"event_id,omitempty"`
}

type DownloadRequest struct {
	URLs  []string `json:"urls"`
	Force bool     `json:"force"`
	// Overrides optionally overlays the job-mutable runtime config for every
	// job created from this request. Omitted fields keep the runtime values;
	// media_user_token overlays catalog.media_user_token for jobs that need it.
	Overrides *config.DownloadOverrides `json:"overrides,omitempty"`
}

type SubmitStatus string

const (
	SubmitAccepted           SubmitStatus = "accepted"
	SubmitInvalid            SubmitStatus = "invalid"
	SubmitDuplicateInRequest SubmitStatus = "duplicate_in_request"
	SubmitDuplicateActive    SubmitStatus = "duplicate_active"
	SubmitQueueFull          SubmitStatus = "queue_full"
)

type SubmitResult struct {
	URL           string       `json:"url"`
	Status        SubmitStatus `json:"status"`
	Job           *Job         `json:"job,omitempty"`
	ExistingJobID string       `json:"existing_job_id,omitempty"`
	Error         string       `json:"error,omitempty"`
}

type BatchSubmitResponse struct {
	Accepted int            `json:"accepted"`
	Rejected int            `json:"rejected"`
	Results  []SubmitResult `json:"results"`
}

type Capability struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Path      string `json:"path,omitempty"`
	Error     string `json:"error,omitempty"`
}
