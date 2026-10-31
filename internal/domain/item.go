package domain

import (
	"encoding/json"
	"path/filepath"
	"time"
)

type ItemStatus string

const (
	ItemQueued          ItemStatus = "queued"
	ItemResolving       ItemStatus = "resolving"
	ItemWaitingDownload ItemStatus = "waiting_download"
	ItemDownloading     ItemStatus = "downloading"
	ItemWaitingDecrypt  ItemStatus = "waiting_decrypt"
	ItemDecrypting      ItemStatus = "decrypting"
	ItemRemuxing        ItemStatus = "remuxing"
	ItemTagging         ItemStatus = "tagging"
	ItemSaving          ItemStatus = "saving"
	ItemCompleted       ItemStatus = "completed"
	ItemFailed          ItemStatus = "failed"
	ItemSkipped         ItemStatus = "skipped_existing"
	ItemCancelled       ItemStatus = "cancelled"
)

// LyricsStatus records the durable outcome of one download attempt's lyrics
// fetch, complementing JobItem.HasLyrics: HasLyrics is the catalog's claim
// that lyrics exist, LyricsStatus is what actually happened when the backend
// tried to get them. Cleared on retry (see ResetForRetry) because the next
// attempt may succeed where this one failed. A skipped item normally stays
// LyricsPending: a track already on disk is skipped before lyrics are fetched,
// unless an earlier codec of the same track fetched them before a later
// codec's output was found on disk.
// On completed items the value intentionally keeps describing the download
// that produced the file, even if a later re-resolve refreshes HasLyrics — the
// skew is meaningful (e.g. HasLyrics=true with LyricsNone: the file predates
// lyrics availability).
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
	// LyricsDisabled: lyrics exist but embed_lyrics is off and lyrics_files
	// is empty, so no fetch was attempted.
	LyricsDisabled LyricsStatus = "disabled"
)

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
	// DownloadBytes is how much of the encrypted media this attempt has
	// received, which is what Download is the share of. It moves with Download
	// (whole-percent steps) and is 0 under the same conditions: before the
	// transfer starts and while it cannot be measured. It is not pinned when
	// the item completes; JobItem.FileSize is the final size.
	DownloadBytes int64 `json:"download_bytes"`
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
	FileSize      int64  `json:"file_size,omitempty"`
	RetryKind     string `json:"retry_kind,omitempty"`
	Attempt       int    `json:"attempt,omitempty"`
	MaxAttempts   int    `json:"max_attempts,omitempty"`
	StatusMessage string `json:"status_message,omitempty"`
	// OutputPath is where the pipeline writes (or found) the track's file,
	// made absolute against the working directory of the run that wrote it, so
	// a later start from another directory still points at the file. Rows
	// written before that may hold a relative path. The pipeline sets it
	// before the file exists; MarshalJSON decides what the API shows.
	OutputPath string    `json:"output_path,omitempty"`
	Error      string    `json:"error,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// MarshalJSON is the one place that decides what the API and the event stream
// say about OutputPath: the absolute path of the file once the track is
// completed or skipped_existing, and nothing before that, because the pipeline
// fills the field before the file exists and keeps it for a failed track.
func (i JobItem) MarshalJSON() ([]byte, error) {
	type plain JobItem
	if i.Finished() {
		i.OutputPath = AbsolutePath(i.OutputPath)
	} else {
		i.OutputPath = ""
	}
	return json.Marshal(plain(i))
}

// AbsolutePath resolves a path against the backend's working directory for
// readers that run elsewhere: API clients, and hook scripts, which may run in
// another workdir or on another machine. An empty or absolute path is returned
// as is.
func AbsolutePath(path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		// Abs fails only when the working directory cannot be read; the
		// relative path is then still the best answer there is.
		return path
	}
	return abs
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
