// Package librarysync watches the signed-in Apple Music library and submits
// the albums of newly added songs as ordinary download jobs.
//
// # Why this cannot be done with timestamps
//
// Apple exposes no per-song add time. library-songs has no dateAdded attribute
// (neither extend= nor fields[] produces one), and library-albums.dateAdded is
// the moment that album's *first* song entered, so it never moves when a later
// song joins the same album — an album added in 2024 that gained a track today
// still reports 2024. sort=-dateModified is rejected outright.
//
// What is dependable is the ORDER returned by sort=-dateAdded. So the watermark
// is positional, not temporal: remember the newest anchorSize song ids, and on
// each pass read newest-first until the oldest remembered id reappears.
// Anything above it that is not remembered was added since. This needs no
// clock, survives clock skew, and is exact rather than approximate.
package librarysync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"amdl/internal/applemusic"
	"amdl/internal/config"
	"amdl/internal/domain"
)

const (
	// anchorSize is how many song ids are remembered. It only has to exceed
	// what can be added between two passes; at the 15-minute default that is a
	// wide margin, and it keeps a single request enough for a quiet pass.
	anchorSize = 50
	// margin is read beyond the anchor so ordinary additions still resolve in
	// one request: anchorSize+margin is within Apple's 100-per-page ceiling.
	margin = 25
	// scanCap bounds a pass that never finds its anchor, so a library whose
	// remembered songs were all deleted cannot pull the whole collection.
	scanCap = 2000
)

// Submitter is the job manager's batch entry point. Using it directly rather
// than the HTTP API means library submissions go through exactly the same
// deduplication, validation, and hook dispatch as manual ones.
type Submitter interface {
	SubmitBatch(ctx context.Context, urls []string, overrides *config.DownloadOverrides) domain.BatchSubmitResponse
}

// AnchorStore persists the positional watermark across restarts.
type AnchorStore interface {
	LoadLibrarySyncAnchor(ctx context.Context) ([]domain.LibraryAnchorEntry, error)
	ReplaceLibrarySyncAnchor(ctx context.Context, entries []domain.LibraryAnchorEntry) error
	ClearLibrarySyncAnchor(ctx context.Context) (int64, error)
}

// Library is the slice of the Apple Music client this package needs.
type Library interface {
	LibrarySongsNewestFirst(ctx context.Context, mediaUserToken string, offset, limit int) ([]applemusic.LibrarySong, bool, error)
	LibraryAlbumCatalogURL(ctx context.Context, mediaUserToken, libraryAlbumID string) (string, bool, error)
}

// Status is the watcher's view of itself, for GET /api/v1/library-sync.
type Status struct {
	Enabled         bool       `json:"enabled"`
	IntervalMinutes int        `json:"interval_minutes"`
	AnchorSize      int        `json:"anchor_size"`
	LastPollAt      *time.Time `json:"last_poll_at,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
	LastSubmitted   int        `json:"last_submitted"`
	TotalSubmitted  int        `json:"total_submitted"`
}

// Watcher polls the library on a schedule taken from the live config, so
// enabling, disabling, and re-timing all take effect without a restart.
type Watcher struct {
	cfg     *config.Store
	library Library
	store   AnchorStore
	jobs    Submitter
	logger  *slog.Logger

	mu             sync.Mutex
	lastPollAt     *time.Time
	lastError      string
	lastSubmitted  int
	totalSubmitted int
	anchorSize     int
	// idleReason is remembered so a watcher that cannot run (no token) says so
	// once per distinct reason rather than every tick.
	idleReason string
}

func New(cfg *config.Store, library Library, store AnchorStore, jobs Submitter, logger *slog.Logger) *Watcher {
	return &Watcher{cfg: cfg, library: library, store: store, jobs: jobs, logger: logger}
}

// Start runs the poll loop until ctx is cancelled.
//
// The loop always ticks at a fixed short cadence and decides per tick whether a
// pass is due. Sleeping for the configured interval instead would mean a change
// from 24 hours to 5 minutes only took effect a day later.
func (w *Watcher) Start(ctx context.Context) {
	go func() {
		const tick = 15 * time.Second
		timer := time.NewTicker(tick)
		defer timer.Stop()
		var lastRun time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			cfg := w.cfg.Get().LibrarySync
			if !cfg.Enabled {
				lastRun = time.Time{} // re-enabling polls promptly, not a full interval later
				w.noteIdle("")
				continue
			}
			if !lastRun.IsZero() && time.Since(lastRun) < cfg.Interval() {
				continue
			}
			lastRun = time.Now()
			w.RunOnce(ctx)
		}
	}()
}

// RunOnce performs a single pass. Exported so the reset endpoint and tests can
// drive it directly.
func (w *Watcher) RunOnce(ctx context.Context) {
	current := w.cfg.Get()
	token := current.Catalog.MediaUserToken
	if token == "" {
		// Not an error worth repeating every tick: nothing is broken, the
		// watcher simply has no credential that can read a personal library.
		w.noteIdle("catalog.media_user_token is empty; the library cannot be read without it")
		return
	}
	w.noteIdle("")
	submitted, err := w.pass(ctx, token)
	now := time.Now()
	w.mu.Lock()
	w.lastPollAt = &now
	w.lastSubmitted = submitted
	w.totalSubmitted += submitted
	if err != nil {
		w.lastError = err.Error()
	} else {
		w.lastError = ""
	}
	w.mu.Unlock()
	if err != nil && !errors.Is(err, context.Canceled) {
		w.logger.Warn("library sync pass failed", "error", err)
	}
}

func (w *Watcher) pass(ctx context.Context, token string) (int, error) {
	anchor, err := w.store.LoadLibrarySyncAnchor(ctx)
	if err != nil {
		return 0, fmt.Errorf("load anchor: %w", err)
	}

	// No anchor: this is a first run or a reset. Record where the library
	// stands and submit nothing — everything present at that moment counts as
	// already had, and back-filling an entire library was never the intent.
	if len(anchor) == 0 {
		songs, _, err := w.library.LibrarySongsNewestFirst(ctx, token, 0, anchorSize)
		if err != nil {
			return 0, fmt.Errorf("read library: %w", err)
		}
		if err := w.store.ReplaceLibrarySyncAnchor(ctx, toEntries(songs)); err != nil {
			return 0, fmt.Errorf("save anchor: %w", err)
		}
		w.setAnchorSize(len(songs))
		w.logger.Info("library sync anchored", "songs", len(songs),
			"newest", firstName(songs), "submitted", 0)
		return 0, nil
	}

	oldest := anchor[len(anchor)-1].SongID
	scanned, foundAnchor, err := w.scanUntil(ctx, token, oldest, len(anchor)+margin)
	if err != nil {
		return 0, fmt.Errorf("read library: %w", err)
	}
	if !foundAnchor {
		// The oldest remembered song is gone from the library, so there is no
		// boundary to measure against. Treating the whole scan as new would
		// queue hundreds of albums, so re-anchor quietly instead.
		w.logger.Warn("library sync anchor not found; re-anchoring without submitting",
			"scanned", len(scanned), "oldest_anchor_song", oldest)
		if err := w.store.ReplaceLibrarySyncAnchor(ctx, toEntries(head(scanned, anchorSize))); err != nil {
			return 0, fmt.Errorf("save anchor: %w", err)
		}
		w.setAnchorSize(min(len(scanned), anchorSize))
		return 0, nil
	}

	// Only songs ahead of the oldest remembered one can be new. The margin read
	// beyond it is older than the anchor by construction; those songs are
	// absent from the anchor merely because it holds a fixed number of entries.
	cut := len(scanned)
	for i, song := range scanned {
		if song.ID == oldest {
			cut = i
			break
		}
	}
	known := make(map[string]bool, len(anchor))
	for _, e := range anchor {
		known[e.SongID] = true
	}
	var added []applemusic.LibrarySong
	for _, song := range scanned[:cut] {
		if !known[song.ID] {
			added = append(added, song)
		}
	}

	submitted := 0
	if len(added) > 0 {
		submitted = w.submitAlbums(ctx, token, added)
	}
	if err := w.store.ReplaceLibrarySyncAnchor(ctx, toEntries(head(scanned, anchorSize))); err != nil {
		return submitted, fmt.Errorf("save anchor: %w", err)
	}
	w.setAnchorSize(min(len(scanned), anchorSize))
	return submitted, nil
}

// scanUntil reads newest-first until untilID reappears, the library ends, or
// scanCap is reached.
func (w *Watcher) scanUntil(ctx context.Context, token, untilID string, want int) ([]applemusic.LibrarySong, bool, error) {
	var out []applemusic.LibrarySong
	for {
		limit := want - len(out)
		if limit <= 0 {
			limit = 100
		}
		page, hasMore, err := w.library.LibrarySongsNewestFirst(ctx, token, len(out), limit)
		if err != nil {
			return out, false, err
		}
		out = append(out, page...)
		for _, song := range page {
			if song.ID == untilID {
				return out, true, nil
			}
		}
		if len(page) == 0 || !hasMore || len(out) >= scanCap {
			return out, false, nil
		}
	}
}

// submitAlbums turns new songs into a deduplicated album submission. Several
// new songs from one album produce one job, and a library album with no catalog
// counterpart is skipped rather than failing the pass.
func (w *Watcher) submitAlbums(ctx context.Context, token string, added []applemusic.LibrarySong) int {
	type albumInfo struct {
		name   string
		artist string
		songs  int
	}
	order := make([]string, 0, len(added))
	albums := make(map[string]*albumInfo, len(added))
	for _, song := range added {
		if song.LibraryAlbumID == "" {
			w.logger.Warn("library song has no album; skipping", "song", song.Name, "song_id", song.ID)
			continue
		}
		info, seen := albums[song.LibraryAlbumID]
		if !seen {
			info = &albumInfo{name: song.AlbumName, artist: song.AlbumArtist}
			albums[song.LibraryAlbumID] = info
			order = append(order, song.LibraryAlbumID)
		}
		info.songs++
	}

	urls := make([]string, 0, len(order))
	for _, albumID := range order {
		info := albums[albumID]
		catalogURL, ok, err := w.library.LibraryAlbumCatalogURL(ctx, token, albumID)
		if err != nil {
			w.logger.Warn("resolve library album failed; skipping",
				"album", info.name, "artist", info.artist, "library_album_id", albumID, "error", err)
			continue
		}
		if !ok {
			w.logger.Info("library album has no catalog entry; skipping",
				"album", info.name, "artist", info.artist, "library_album_id", albumID)
			continue
		}
		urls = append(urls, catalogURL)
		w.logger.Info("library addition detected",
			"album", info.name, "artist", info.artist, "new_songs", info.songs, "url", catalogURL)
	}
	if len(urls) == 0 {
		return 0
	}
	// No overrides: these are ordinary jobs, so they honour the same runtime
	// config and the same hooks as anything submitted by hand.
	resp := w.jobs.SubmitBatch(ctx, urls, nil)
	for _, result := range resp.Results {
		if result.Status != domain.SubmitAccepted {
			w.logger.Info("library submission not queued",
				"url", result.URL, "status", string(result.Status))
		}
	}
	w.logger.Info("library sync submitted albums",
		"accepted", resp.Accepted, "rejected", resp.Rejected)
	return resp.Accepted
}

// ResetAnchor forgets the watermark and reports how many entries went.
//
// This forgets history rather than replaying it: the next pass finds no anchor,
// re-anchors to the library as it stands then, and submits nothing. Use it when
// the watcher's idea of "already had" has drifted from reality — not to
// re-download something, which is what submitting the album directly is for.
func (w *Watcher) ResetAnchor(ctx context.Context) (int64, error) {
	cleared, err := w.store.ClearLibrarySyncAnchor(ctx)
	if err != nil {
		return 0, err
	}
	w.setAnchorSize(0)
	w.logger.Info("library sync anchor reset", "cleared", cleared)
	return cleared, nil
}

// Status reports the watcher's current state, merging live config with what the
// last pass observed.
func (w *Watcher) Status() Status {
	cfg := w.cfg.Get().LibrarySync
	w.mu.Lock()
	defer w.mu.Unlock()
	return Status{
		Enabled:         cfg.Enabled,
		IntervalMinutes: cfg.IntervalMinutes,
		AnchorSize:      w.anchorSize,
		LastPollAt:      w.lastPollAt,
		LastError:       w.lastError,
		LastSubmitted:   w.lastSubmitted,
		TotalSubmitted:  w.totalSubmitted,
	}
}

func (w *Watcher) setAnchorSize(n int) {
	w.mu.Lock()
	w.anchorSize = n
	w.mu.Unlock()
}

// noteIdle logs a reason for not polling only when it changes, so a watcher
// left enabled without a token does not fill the log.
func (w *Watcher) noteIdle(reason string) {
	w.mu.Lock()
	changed := w.idleReason != reason
	w.idleReason = reason
	if reason != "" {
		w.lastError = reason
	}
	w.mu.Unlock()
	if changed && reason != "" {
		w.logger.Warn("library sync idle", "reason", reason)
	}
}

func toEntries(songs []applemusic.LibrarySong) []domain.LibraryAnchorEntry {
	out := make([]domain.LibraryAnchorEntry, 0, len(songs))
	for _, song := range songs {
		out = append(out, domain.LibraryAnchorEntry{
			SongID: song.ID, Name: song.Name, AlbumName: song.AlbumName,
		})
	}
	return out
}

func head(songs []applemusic.LibrarySong, n int) []applemusic.LibrarySong {
	if len(songs) <= n {
		return songs
	}
	return songs[:n]
}

func firstName(songs []applemusic.LibrarySong) string {
	if len(songs) == 0 {
		return ""
	}
	return songs[0].Name
}
