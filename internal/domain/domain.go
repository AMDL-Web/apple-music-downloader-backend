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

// MotionArtwork is one album's animated covers plus the palette belonging to
// each variant. Grouped rather than passed as a dozen loose strings, since the
// URL and its palette must always travel together — pairing a motion asset with
// the still cover's palette is exactly the bug this prevents.
type MotionArtwork struct {
	SquareURL    string
	TallURL      string
	SquareColors ArtworkPalette
	TallColors   ArtworkPalette
}

// ArtworkPalette is a background color plus four text colors, hex without "#".
type ArtworkPalette struct {
	BgColor    string
	TextColor1 string
	TextColor2 string
	TextColor3 string
	TextColor4 string
}

type Job struct {
	ID         string `json:"id"`
	Input      string `json:"input"`
	Type       string `json:"type"`
	Storefront string `json:"storefront,omitempty"`
	Title      string `json:"title,omitempty"`
	ArtworkURL string `json:"artwork_url"`
	// Display metadata resolved from the Apple Music catalog alongside
	// Title/ArtworkURL. Which fields are populated depends on Type:
	// album/song fill ArtistName/ReleaseDate/Genre, playlist fills
	// CuratorName, artist fills ArtistName (the artist's own name), and
	// station fills CuratorName with the station provider. All are optional;
	// jobs resolved before these fields existed keep them empty.
	ArtistName  string `json:"artist_name,omitempty"`
	CuratorName string `json:"curator_name,omitempty"`
	ReleaseDate string `json:"release_date,omitempty"`
	Genre       string `json:"genre,omitempty"`
	// ArtworkBgColor/ArtworkTextColor1..4 carry the color palette Apple
	// attaches to the resolved collection's artwork (attributes.artwork
	// bgColor/textColor1..4): hex strings without a leading "#". Resolved
	// next to ArtworkURL; empty when the catalog omits the palette or the
	// job was resolved before these fields existed.
	ArtworkBgColor    string `json:"artwork_bg_color,omitempty"`
	ArtworkTextColor1 string `json:"artwork_text_color1,omitempty"`
	ArtworkTextColor2 string `json:"artwork_text_color2,omitempty"`
	ArtworkTextColor3 string `json:"artwork_text_color3,omitempty"`
	ArtworkTextColor4 string `json:"artwork_text_color4,omitempty"`
	// MotionArtworkURL/MotionArtworkTallURL are HLS master playlists for the
	// animated cover Apple Music shows on albums that have one (1:1 and 3:4).
	// Public, unsigned URLs a client can hand straight to a player.
	//
	// Only album and song jobs can have them, and only some albums do. They are
	// filled in asynchronously after the input resolves, so a job that has just
	// started will report them empty and gain them a moment later — clients must
	// treat "absent" as "no animated cover" and re-render when the job updates.
	MotionArtworkURL     string `json:"motion_artwork_url,omitempty"`
	MotionArtworkTallURL string `json:"motion_artwork_tall_url,omitempty"`
	// Each motion variant carries its own palette, taken from that variant's
	// previewFrame — not from the still cover. They differ sharply: one album
	// reports 598090 with near-black text for the still, 5c6786 with near-white
	// for the square loop, and 05104b with light text for the tall one. Pair the
	// palette with the asset actually on screen or you get dark text on a dark
	// video. Empty whenever the matching URL is empty.
	MotionArtworkBgColor        string `json:"motion_artwork_bg_color,omitempty"`
	MotionArtworkTextColor1     string `json:"motion_artwork_text_color1,omitempty"`
	MotionArtworkTextColor2     string `json:"motion_artwork_text_color2,omitempty"`
	MotionArtworkTextColor3     string `json:"motion_artwork_text_color3,omitempty"`
	MotionArtworkTextColor4     string `json:"motion_artwork_text_color4,omitempty"`
	MotionArtworkTallBgColor    string `json:"motion_artwork_tall_bg_color,omitempty"`
	MotionArtworkTallTextColor1 string `json:"motion_artwork_tall_text_color1,omitempty"`
	MotionArtworkTallTextColor2 string `json:"motion_artwork_tall_text_color2,omitempty"`
	MotionArtworkTallTextColor3 string `json:"motion_artwork_tall_text_color3,omitempty"`
	MotionArtworkTallTextColor4 string `json:"motion_artwork_tall_text_color4,omitempty"`
	CanonicalKey                string `json:"-"`
	// Force is legacy: it was the submission-time overwrite flag before
	// download.force_overwrite existed as a global config key with a
	// per-request override (Overrides.ForceOverwrite). New jobs never set it;
	// it is kept so jobs persisted before the migration still force-overwrite
	// on retry and post-restart requeue.
	Force bool `json:"force"`
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

// ItemProgress is a per-item breakdown of the download pipeline. It replaced a
// single 0..1 aggregate that spanned every stage on one axis, which forced two
// unrelated measurements (bytes off the CDN, bytes through the decryptor) into
// shared sub-ranges and reduced the atomic stages to fixed points on that axis
// — 0.97 never meant "97% tagged", only "tagging started".
//
// So the two stages that genuinely have a fraction each get their own meter,
// and everything else is a plain done/not-done flag. There is deliberately no
// overall percentage: with the stages separated, any single number would have
// to re-invent the arbitrary weighting this type exists to remove. A client
// that wants one bar should pick a weighting that suits its own UI.
//
// Zero value = nothing has run yet, which is also what a queued item reports.
// Read Status, not this, to decide whether an item is finished: a
// skipped_existing item reports Resolved (the catalog lookup is what produced
// the path found to already exist) with every later stage false, because the
// file was on disk and nothing downloaded, decrypted, or wrote it.
type ItemProgress struct {
	// Download and Decrypt are fractions in [0,1] of their stage. Both stay at
	// 0 while the stage cannot be measured — a media response without a
	// Content-Length never yields a fraction — so a 0 here alongside
	// status=downloading means "running, size unknown", not "no bytes yet".
	// Both are pinned to 1 when the item completes, whatever they reported
	// along the way.
	Download float64 `json:"download"`
	Decrypt  float64 `json:"decrypt"`
	// Resolved: catalog metadata for the track was fetched.
	Resolved bool `json:"resolved"`
	// Remuxed: the decrypted stream was flattened into a progressive MP4.
	Remuxed bool `json:"remuxed"`
	// Verified: the integrity check ran and passed (for ALAC, possibly after a
	// successful repair). Also false when the check is switched off by
	// download.check_integrity, so false means "not verified", never "corrupt"
	// — a failed check fails the item instead.
	Verified bool `json:"verified"`
	// Tagged: metadata (and cover/lyrics, per config) was written into the file.
	Tagged bool `json:"tagged"`
	// Saved: the finished file was moved to its final output path. This is the
	// last step, so Saved is true for exactly the items that completed.
	Saved bool `json:"saved"`
}

// ResetForAttempt clears everything a fresh download attempt has to earn again,
// keeping only Resolved: the catalog metadata was fetched once and stays valid
// across codec fallbacks and retries. Without this a codec that failed late —
// say at the integrity check, with remuxed already true — would leave those
// flags set while the replacement codec is still downloading, and an
// unmeasurable transfer would inherit the previous attempt's full meter.
func (p *ItemProgress) ResetForAttempt() {
	resolved := p.Resolved
	*p = ItemProgress{Resolved: resolved}
}

// CompleteTransfers pins both meters to 1 for an item that reached completed.
// A completed track has by definition finished downloading and decrypting, but
// neither meter is guaranteed to have said so: an unmeasurable transfer never
// reports a fraction. Verified is deliberately not forced — false there means
// the integrity check never ran, which stays true of a completed item.
func (p *ItemProgress) CompleteTransfers() {
	p.Download = 1
	p.Decrypt = 1
}

type JobItem struct {
	ID     string `json:"id"`
	JobID  string `json:"job_id"`
	AdamID string `json:"adam_id"`
	Kind   string `json:"kind"`
	Index  int    `json:"index"`
	Title  string `json:"title,omitempty"`
	Artist string `json:"artist,omitempty"`
	Album  string `json:"album,omitempty"`
	// DurationMS is the track's playback duration in milliseconds, resolved
	// from the Apple Music catalog alongside Title/Artist/Album (so it is
	// available before the download runs). 0/absent for items produced before
	// this field existed or when the catalog omits it. Unlike the quality and
	// file_size fields it is stable catalog metadata, so a retry keeps it.
	DurationMS   int          `json:"duration_ms,omitempty"`
	ArtworkURL   string       `json:"artwork_url"`
	HasLyrics    bool         `json:"has_lyrics"`
	LyricsStatus LyricsStatus `json:"lyrics_status,omitempty"`
	Status       ItemStatus   `json:"status"`
	Progress     ItemProgress `json:"progress"`
	Codec        string       `json:"codec,omitempty"`
	BitDepth     int          `json:"bit_depth,omitempty"`
	SampleRate   int          `json:"sample_rate,omitempty"`
	Bitrate      int          `json:"bitrate,omitempty"`
	// FileSize is the size in bytes of the final output file, set once the
	// track is written to disk (completed) or found already present
	// (skipped_existing). Zero until then, and for items produced before this
	// field existed or whose file the backend could not stat.
	FileSize      int64     `json:"file_size,omitempty"`
	RetryKind     string    `json:"retry_kind,omitempty"`
	Attempt       int       `json:"attempt,omitempty"`
	MaxAttempts   int       `json:"max_attempts,omitempty"`
	StatusMessage string    `json:"status_message,omitempty"`
	OutputPath    string    `json:"-"`
	Error         string    `json:"error,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
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
	i.Progress = ItemProgress{}
	i.Codec = ""
	i.BitDepth, i.SampleRate, i.Bitrate = 0, 0, 0
	i.FileSize = 0
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

// MarshalEventPayload serializes a public API snapshot and overlays
// event-specific fields on the resulting JSON object. Event payloads use this
// helper so a Job/JobItem snapshot stays aligned with the REST representation
// as fields are added, while existing event-specific keys remain available to
// clients. Custom MarshalJSON implementations (notably Job's credential
// redaction) are honored before the overlay is applied.
func MarshalEventPayload(snapshot any, fields map[string]any) string {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return ""
	}
	payload := make(map[string]json.RawMessage)
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	for key, value := range fields {
		encoded, err := json.Marshal(value)
		if err != nil {
			return ""
		}
		payload[key] = encoded
	}
	raw, err = json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(raw)
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
	URLs []string `json:"urls"`
	// Overrides optionally overlays the job-mutable runtime config for every
	// job created from this request. Omitted fields keep the runtime values;
	// media_user_token overlays catalog.media_user_token for jobs that need
	// it, force_overwrite overlays download.force_overwrite, and hooks limits
	// hook dispatch to named configured entries. The former request-level
	// `force` field moved into overrides.force_overwrite and is now rejected as
	// an unknown field.
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
