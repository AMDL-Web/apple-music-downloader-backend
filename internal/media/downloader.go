package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"amdl/internal/applemusic"
	"amdl/internal/concurrency"
	"amdl/internal/config"
	"amdl/internal/domain"
	"amdl/internal/jobs"
	"amdl/internal/logging"
	"amdl/internal/storage"
	"amdl/internal/wrapper"
)

type Downloader struct {
	// store is the live runtime config; every ProcessJob/ValidateRequest call
	// reads a fresh snapshot from it. cfg is the effective config the current
	// operation runs under (snapshot plus the job's overrides — see
	// withConfig); it doubles as the fallback when store is nil in unit tests
	// that build a Downloader literal around a fixed config.
	store   *config.Store
	cfg     config.Config
	catalog downloaderCatalog
	wrapper downloaderWrapper
	tools   *ToolChecker
	http    *http.Client
	mp4     *MP4Processor
	covers  *coverCache
	logger  *slog.Logger

	// Track concurrency is intentionally allowed up to 64, but the Apple
	// Catalog and media CDN have lower independent capacity ceilings. These
	// process-wide gates are shared by per-job clones so concurrent jobs cannot
	// multiply their upstream pressure.
	metadataLimiter *concurrency.Limiter
	mediaLimiter    *concurrency.Limiter

	// Per-job suppression for standalone cover paths that were already handled
	// (written, unavailable, or exhausted). Access is serialized by
	// standaloneCoverMu.
	standaloneCoverHandled map[string]struct{}
}

type downloaderCatalog interface {
	Song(context.Context, string, string) (applemusic.Song, error)
	SongMetadata(context.Context, string, string) (applemusic.Song, error)
	Album(context.Context, string, string) (applemusic.Collection, error)
	Playlist(context.Context, string, string, string) (applemusic.Collection, error)
	StationTracks(context.Context, string, string, string) (applemusic.Collection, error)
	ArtistAlbums(context.Context, string, string) (applemusic.ArtistAlbums, error)
	Artist(context.Context, string, string) (applemusic.Artist, error)
	FetchCover(context.Context, []string, string, string) ([]byte, error)
	EnhancedHLSViaWebToken(context.Context, string, string) (string, error)
}

type downloaderWrapper interface {
	Status(context.Context) (wrapper.Status, error)
	M3U8(context.Context, string) (string, error)
	Lyrics(context.Context, string, wrapper.LyricsRequestOptions) (string, error)
	WebPlayback(context.Context, string) (string, error)
	NewDecryptSession(context.Context, string) (wrapper.DecryptSession, error)
	License(context.Context, string, string, string) (string, error)
}

type selectedDownloadMedia struct {
	info selectedMediaInfo
	// raw holds the still-encrypted whole track only in high-memory mode. Keeping
	// one immutable copy allows decrypt retries without another CDN request while
	// avoiding the raw-* scratch file and its write/read round trip.
	raw []byte
	// rawPath is the on-disk location of the still-encrypted media downloaded by
	// downloadSelectedEnhancedMedia in low-memory mode. It is kept on disk so a
	// whole Hi-Res track's encrypted bytes aren't pinned across the decrypt phase,
	// and so a decrypt-phase retry can re-read them without re-fetching.
	rawPath string
}

func NewDownloader(store *config.Store, catalog *applemusic.CatalogClient, wrapperClient *wrapper.Client, tools *ToolChecker, logger *slog.Logger) *Downloader {
	cfg := store.Get()
	return &Downloader{
		store: store, cfg: cfg, catalog: catalog, wrapper: wrapperClient, tools: tools,
		http: newHTTPClient(), mp4: newMP4Processor(cfg), logger: logger,
		metadataLimiter: concurrency.NewLimiter(func() int { return store.Get().Download.MaxParallelMetadataRequests }),
		mediaLimiter:    concurrency.NewLimiter(func() int { return store.Get().Download.MaxParallelMediaDownloads }),
	}
}

// baseConfig returns the current runtime config, falling back to the fixed
// cfg field for test Downloaders constructed without a store.
func (d *Downloader) baseConfig() config.Config {
	if d.store != nil {
		return d.store.Get()
	}
	return d.cfg
}

// withConfig returns a shallow copy of d bound to cfg, so one operation's
// effective config (runtime snapshot plus per-job overrides) never leaks into
// other jobs running concurrently on the shared Downloader. The MP4 processor
// is rebuilt because it captures config (temp dir, embed_cover) itself.
func (d *Downloader) withConfig(cfg config.Config) *Downloader {
	clone := *d
	clone.cfg = cfg
	clone.mp4 = newMP4Processor(cfg)
	clone.covers = nil
	return &clone
}

func (d *Downloader) ValidateRequest(ctx context.Context, url string) (jobs.ValidationResult, error) {
	return d.withConfig(d.baseConfig()).validateRequest(ctx, url)
}

func (d *Downloader) validateRequest(ctx context.Context, url string) (jobs.ValidationResult, error) {
	parsed, err := applemusic.ParseWithAlbumTrackMode(url, d.cfg.Catalog.AlbumTrackURLMode)
	if err != nil {
		if strings.Contains(err.Error(), "album_track_url_mode") {
			return jobs.ValidationResult{}, &jobs.RequestError{Code: "invalid_configuration", Message: err.Error(), Cause: err}
		}
		return jobs.ValidationResult{}, &jobs.RequestError{Code: "invalid_url", Message: err.Error(), Cause: err}
	}
	if parsed.Type == applemusic.TypeVideo {
		message := fmt.Sprintf("%s download is not implemented", parsed.Type)
		return jobs.ValidationResult{}, &jobs.RequestError{Code: "unsupported_input", Message: message}
	}
	if err := d.validateStorefront(ctx, parsed.Storefront); err != nil {
		return jobs.ValidationResult{}, err
	}
	return jobs.ValidationResult{Type: string(parsed.Type), Storefront: parsed.Storefront, ID: parsed.ID}, nil
}

func (d *Downloader) validateStorefront(ctx context.Context, storefront string) error {
	if d.cfg.Simulate.Enabled {
		// Test mode never decrypts, so it must not depend on a running
		// wrapper/decryptor or its supported-storefront list.
		return nil
	}
	status, err := d.wrapper.Status(ctx)
	if err != nil {
		message := fmt.Sprintf("failed to check decryptor status: %v", err)
		return &jobs.RequestError{Code: "decryptor_unavailable", Message: message, Cause: err}
	}
	if len(status.Regions) == 0 && (!status.Ready || !status.Status) {
		return &jobs.RequestError{Code: "decryptor_unavailable", Message: "decryptor is not ready"}
	}
	for _, region := range status.Regions {
		if strings.EqualFold(region, storefront) {
			if !status.Ready || !status.Status {
				return &jobs.RequestError{Code: "decryptor_unavailable", Message: "decryptor is not ready"}
			}
			return nil
		}
	}
	message := fmt.Sprintf("storefront %q is not supported by decryptor (supported: %s)", storefront, strings.Join(status.Regions, ", "))
	return &jobs.RequestError{
		Code: "unsupported_storefront", Message: message, Storefront: storefront,
		SupportedStorefronts: append([]string(nil), status.Regions...),
	}
}

func (d *Downloader) ProcessJob(ctx context.Context, job domain.Job, reporter jobs.Reporter) error {
	// Bind this job to its effective config: the live runtime snapshot with
	// the job's submission-time overrides layered on top. Read once here so
	// a concurrent runtime-config update never changes behavior mid-job.
	cfg, err := job.Overrides.ApplyValidated(d.baseConfig())
	if err != nil {
		return fmt.Errorf("invalid job overrides: %w", err)
	}
	if !cfg.Simulate.Enabled {
		// The runtime config or the job's overrides may point at directories
		// that main() did not create at startup.
		if err := os.MkdirAll(cfg.Download.DownloadsDir, 0o755); err != nil {
			return fmt.Errorf("create downloads dir: %w", err)
		}
		if err := os.MkdirAll(cfg.Download.TempDir, 0o755); err != nil {
			return fmt.Errorf("create temp dir: %w", err)
		}
	}
	jobDownloader := d.withConfig(cfg)
	jobDownloader.covers = newCoverCache(jobDownloader.catalog)
	jobDownloader.standaloneCoverHandled = make(map[string]struct{})
	return jobDownloader.processJob(ctx, job, reporter)
}

// parseJobInput reconstructs the submission-time parse result from the job's
// canonical key ("type:storefront:id", written by the manager after
// ValidateRequest). Re-parsing the raw input here instead would apply the
// CURRENT catalog.album_track_url_mode, so an album?i= link submitted as a
// song could silently turn into a whole-album job (or vice versa) if that
// mode changed while the job sat in the queue — diverging from the dedup key
// and metadata stored at submission. Jobs without a well-formed key fall
// back to re-parsing.
func parseJobInput(job domain.Job, albumTrackURLMode string) (applemusic.ParsedURL, error) {
	parts := strings.SplitN(job.CanonicalKey, ":", 3)
	if len(parts) == 3 && parts[0] != "" && parts[1] != "" && parts[2] != "" {
		return applemusic.ParsedURL{Raw: job.Input, Type: applemusic.URLType(parts[0]), Storefront: parts[1], ID: parts[2]}, nil
	}
	return applemusic.ParseWithAlbumTrackMode(job.Input, albumTrackURLMode)
}

func (d *Downloader) processJob(ctx context.Context, job domain.Job, reporter jobs.Reporter) error {
	parsed, err := parseJobInput(job, d.cfg.Catalog.AlbumTrackURLMode)
	if err != nil {
		return err
	}
	if parsed.Type == applemusic.TypeVideo {
		return fmt.Errorf("%s download is not implemented in core phase", parsed.Type)
	}

	// The wrapper may change between submission and execution, so retain a
	// defensive check even though Submit already validated this storefront.
	if err := d.validateStorefront(ctx, parsed.Storefront); err != nil {
		return err
	}
	job.Type = string(parsed.Type)
	job.Storefront = parsed.Storefront
	if err := reporter.SetJob(ctx, job); err != nil {
		return err
	}

	resolved, _, err := retryValue(ctx, d.cfg.Download.MaxAttempts, retryBackoff, func(int) (resolvedCollection, error) {
		return d.resolveCollection(ctx, parsed)
	}, func(failure retryFailure) {
		d.emitRetryEvent(ctx, reporter, job.ID, "", "resolve_tracks", "", failure)
	})
	if err != nil {
		return err
	}
	tracks := resolved.Tracks
	collectionName := resolved.Name
	collectionID := resolved.ID
	// Simulate mode never fetches artwork or writes files, so the standalone
	// playlist cover is skipped along with the per-track disk writes.
	if (parsed.Type == applemusic.TypePlaylist || parsed.Type == applemusic.TypeStation) && d.cfg.Download.SavePlaylistCover && len(tracks) > 0 && !d.cfg.Simulate.Enabled {
		folder := collectionFolderPath(d.cfg, tracks[0], parsed.Type, collectionName, collectionID)
		if coverErr := d.savePlaylistCover(ctx, resolved.ArtworkURL, folder); coverErr != nil {
			_ = reporter.Event(ctx, domain.Event{JobID: job.ID, Type: "standalone_cover_failed", Phase: "playlist_cover", Message: coverErr.Error()})
		}
	}
	job.TotalItems = len(tracks)
	job.Title = resolved.Name
	job.ArtworkURL = resolved.ArtworkURL
	if err := reporter.SetJob(ctx, job); err != nil {
		return err
	}
	// Emit after title/total_items/artwork are persisted so the overview feed
	// can push a download_upserted with the real name (not just the URL).
	if err := reporter.Event(ctx, domain.Event{JobID: job.ID, Type: "resolved_input", Message: string(parsed.Type)}); err != nil {
		return err
	}
	if len(tracks) == 0 {
		return fmt.Errorf("no downloadable songs found")
	}
	folderArtist := collectionFolderArtist(parsed.Type, tracks)

	items, finished, err := syncJobItems(ctx, job, tracks, reporter)
	if err != nil {
		return err
	}
	metadata := newTrackMetadataResolver(d, parsed.Storefront)

	parallel := d.cfg.Download.MaxParallelTracks
	if parallel <= 0 {
		parallel = 1
	}
	return runParallelTrackTasks(ctx, len(tracks), parallel, finished, func(i int) error {
		err := d.processTrackWithMetadata(ctx, job, items[i], tracks[i], parsed.Storefront, parsed.Type, collectionName, collectionID, i+1, folderArtist, metadata, reporter)
		if err != nil {
			logging.FromContext(ctx, d.logger).Error("track failed", "item_id", items[i].ID, "adam_id", tracks[i].ID, "error", err)
		}
		return err
	})
}

// runParallelTrackTasks bounds track concurrency and, critically, does not
// return while any task it started is still running. A cancellation can arrive
// while the caller is waiting for a semaphore slot; in that case no more tasks
// are launched and the already-started tasks are joined before returning.
func runParallelTrackTasks(ctx context.Context, total, parallel int, finished []bool, task func(int) error) error {
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for i := 0; i < total; i++ {
		if finished[i] {
			// Finished in a previous run of this job; keep the item as-is.
			continue
		}
		select {
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		case sem <- struct{}{}:
		}
		// Cancellation and a free semaphore slot may become ready together.
		// Re-check before launching so cancellation never knowingly starts the
		// next track after acquiring the slot.
		if err := ctx.Err(); err != nil {
			<-sem
			wg.Wait()
			return err
		}
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := task(i); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return firstErr
}

// syncJobItems reconciles the resolved track list with the item rows a
// previous run of this job may have left behind (a retry of a failed job, or
// a requeue after a backend restart), instead of inserting duplicates. Items
// that already finished (completed/skipped) keep their state and are flagged
// in the returned finished slice so the caller excludes them from
// downloading; everything else is reset to queued and re-processed under its
// original item id. Rows whose track no longer appears in the resolved
// collection (e.g. a playlist edited between runs) are removed so the
// progress counters stay consistent with total_items. Tracks never seen
// before get a fresh item row, as on a first run.
func syncJobItems(ctx context.Context, job domain.Job, tracks []applemusic.Song, reporter jobs.Reporter) ([]domain.JobItem, []bool, error) {
	previous, err := reporter.ListItems(ctx, job.ID)
	if err != nil {
		return nil, nil, err
	}
	previousByAdamID := make(map[string][]domain.JobItem, len(previous))
	for _, item := range previous {
		previousByAdamID[item.AdamID] = append(previousByAdamID[item.AdamID], item)
	}
	takePrevious := func(adamID string) (domain.JobItem, bool) {
		queued := previousByAdamID[adamID]
		if len(queued) == 0 {
			return domain.JobItem{}, false
		}
		previousByAdamID[adamID] = queued[1:]
		return queued[0], true
	}

	items := make([]domain.JobItem, len(tracks))
	finished := make([]bool, len(tracks))
	for i, track := range tracks {
		if prev, ok := takePrevious(track.ID); ok {
			indexChanged := prev.Index != i+1
			lyricsChanged := prev.HasLyrics != track.HasLyrics
			prev.Index = i + 1
			// has_lyrics is documented as set at collection resolve, so reused
			// rows (retries, requeues, rows predating the field) take the fresh
			// catalog value instead of exposing a stale flag until the per-track
			// metadata refresh — which finished items never reach.
			prev.HasLyrics = track.HasLyrics
			if prev.Finished() {
				finished[i] = true
			} else {
				prev.ResetForRetry()
			}
			items[i] = prev
			if !finished[i] || indexChanged || lyricsChanged {
				if err := reporter.UpdateItem(ctx, prev); err != nil {
					return nil, nil, err
				}
			}
			continue
		}
		items[i] = domain.JobItem{
			ID: storage.NewID("item"), JobID: job.ID, AdamID: track.ID, Kind: "song", Index: i + 1,
			Title: track.Name, Artist: track.ArtistName, Album: track.AlbumName,
			ArtworkURL: firstNonEmpty(track.ArtworkURL, track.AlbumArtworkURL), HasLyrics: track.HasLyrics,
			Status: domain.ItemQueued,
		}
		if err := reporter.AddItem(ctx, items[i]); err != nil {
			return nil, nil, err
		}
	}
	for _, leftover := range previousByAdamID {
		for _, stale := range leftover {
			if err := reporter.RemoveItem(ctx, stale.ID); err != nil {
				return nil, nil, err
			}
		}
	}
	return items, finished, nil
}

func collectionFolderArtist(collectionType applemusic.URLType, tracks []applemusic.Song) string {
	if outputCollectionType(collectionType) != applemusic.TypeAlbum || len(tracks) == 0 {
		return ""
	}
	if tracks[0].AlbumArtist != "" {
		return tracks[0].AlbumArtist
	}
	return tracks[0].ArtistName
}

func outputCollectionType(collectionType applemusic.URLType) applemusic.URLType {
	if collectionType == applemusic.TypeArtist {
		return applemusic.TypeAlbum
	}
	return collectionType
}

type resolvedCollection struct {
	Tracks     []applemusic.Song
	ID         string
	Name       string
	ArtworkURL string
}

// mediaUserToken returns the token from this job's effective config. ProcessJob
// has already layered overrides.media_user_token over the current runtime
// catalog value, so retries and post-restart recovery follow the same path.
func (d *Downloader) mediaUserToken() string {
	return strings.TrimSpace(d.cfg.Catalog.MediaUserToken)
}

func (d *Downloader) resolveCollection(ctx context.Context, parsed applemusic.ParsedURL) (resolvedCollection, error) {
	switch parsed.Type {
	case applemusic.TypeSong:
		song, err := d.catalog.Song(ctx, parsed.Storefront, parsed.ID)
		if err != nil {
			return resolvedCollection{}, err
		}
		return resolvedCollection{Tracks: []applemusic.Song{song}, Name: song.Name, ArtworkURL: firstNonEmpty(song.ArtworkURL, song.AlbumArtworkURL)}, nil
	case applemusic.TypeAlbum:
		album, err := d.catalog.Album(ctx, parsed.Storefront, parsed.ID)
		if err != nil {
			return resolvedCollection{}, err
		}
		return resolvedCollection{Tracks: album.Tracks, ID: album.ID, Name: album.Name, ArtworkURL: album.ArtworkURL}, nil
	case applemusic.TypePlaylist:
		playlist, err := d.catalog.Playlist(ctx, parsed.Storefront, parsed.ID, d.mediaUserToken())
		if err != nil {
			return resolvedCollection{}, err
		}
		return resolvedCollection{Tracks: playlist.Tracks, ID: playlist.ID, Name: playlist.Name, ArtworkURL: playlist.ArtworkURL}, nil
	case applemusic.TypeStation:
		station, err := d.catalog.StationTracks(ctx, parsed.Storefront, parsed.ID, d.mediaUserToken())
		if err != nil {
			return resolvedCollection{}, err
		}
		return resolvedCollection{Tracks: station.Tracks, ID: station.ID, Name: station.Name, ArtworkURL: station.ArtworkURL}, nil
	case applemusic.TypeArtist:
		artist, err := d.catalog.ArtistAlbums(ctx, parsed.Storefront, parsed.ID)
		if err != nil {
			return resolvedCollection{}, err
		}
		tracks, err := d.artistTracks(ctx, parsed.Storefront, artist.Albums)
		if err != nil {
			return resolvedCollection{}, err
		}
		return resolvedCollection{Tracks: tracks, ID: artist.ID, Name: artist.Name, ArtworkURL: artist.ArtworkURL}, nil
	default:
		return resolvedCollection{}, fmt.Errorf("unsupported input type %s", parsed.Type)
	}
}

func (d *Downloader) artistTracks(ctx context.Context, storefront string, albums []applemusic.Collection) ([]applemusic.Song, error) {
	tracks := make([]applemusic.Song, 0)
	for _, summary := range albums {
		album, err := d.catalog.Album(ctx, storefront, summary.ID)
		if err != nil {
			return nil, err
		}
		// Keep the complete track list of every album. A catalog song may be
		// related to more than one album with the same Adam ID; artist downloads
		// still need one output in each album directory.
		tracks = append(tracks, album.Tracks...)
	}
	return tracks, nil
}

type albumMetadataResult struct {
	done  chan struct{}
	album applemusic.Collection
	err   error
}

// trackMetadataResolver lives for one job. It keeps the intentional per-track
// song refresh while coalescing album enrichment shared by playlist tracks.
// Album and artist jobs already carry that enrichment in their resolved track
// values, so those paths never read the same album again here.
type trackMetadataResolver struct {
	downloader *Downloader
	storefront string

	mu     sync.Mutex
	albums map[string]*albumMetadataResult
}

func newTrackMetadataResolver(d *Downloader, storefront string) *trackMetadataResolver {
	return &trackMetadataResolver{
		downloader: d,
		storefront: storefront,
		albums:     make(map[string]*albumMetadataResult),
	}
}

func (r *trackMetadataResolver) song(ctx context.Context, initial applemusic.Song, collectionType applemusic.URLType) (applemusic.Song, error) {
	// A single-song resolve already used CatalogClient.Song, including its album
	// enrichment, immediately before processTrack. Reusing it avoids fetching
	// both the song and its album twice. The name guard retains a defensive
	// fallback for callers that provide only an Adam ID.
	if collectionType == applemusic.TypeSong && strings.TrimSpace(initial.Name) != "" {
		if initial.AlbumID != "" && (initial.TrackCount == 0 || initial.DiscCount == 0) {
			// Song's album enrichment is best-effort. If it failed during the
			// collection resolve, retain the old pipeline's recovery opportunity
			// without repeating the successful song request.
			if album, err := r.album(ctx, initial.AlbumID); err == nil && len(album.Tracks) > 0 {
				initial = enrichTrackWithAlbum(initial, album)
			}
		}
		return initial, nil
	}

	release, err := acquireOperationSlot(ctx, r.downloader.metadataLimiter)
	if err != nil {
		return applemusic.Song{}, err
	}
	song, err := r.downloader.catalog.SongMetadata(ctx, r.storefront, initial.ID)
	release()
	if err != nil {
		return applemusic.Song{}, err
	}
	song = mergeResolvedSong(song, initial)
	if collectionType == applemusic.TypeAlbum || collectionType == applemusic.TypeArtist {
		// Album track relationships are authoritative for placement. In
		// particular, the same catalog song ID can belong to multiple albums;
		// the lightweight song refresh may describe only its default album and
		// must not redirect every occurrence into that album's output path.
		song = preserveCollectionTrackContext(song, initial)
	}
	if (collectionType != applemusic.TypePlaylist && collectionType != applemusic.TypeStation) || song.AlbumID == "" {
		return song, nil
	}

	// Playlist and station track payloads do not carry the collection-level
	// album fields available to album and artist resolves (station tracks come
	// from the rolling next-tracks feed, whose album relationship lacks
	// per-track disc totals and the album artist id). Preserve Song's
	// historical best-effort enrichment, but share one album read across every
	// track from that album and every concurrent worker in this job.
	album, err := r.album(ctx, song.AlbumID)
	if err == nil && len(album.Tracks) > 0 {
		song = enrichTrackWithAlbum(song, album)
	}
	return song, nil
}

func (r *trackMetadataResolver) album(ctx context.Context, albumID string) (applemusic.Collection, error) {
	r.mu.Lock()
	if pending, ok := r.albums[albumID]; ok {
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return applemusic.Collection{}, ctx.Err()
		case <-pending.done:
			return pending.album, pending.err
		}
	}
	pending := &albumMetadataResult{done: make(chan struct{})}
	r.albums[albumID] = pending
	r.mu.Unlock()

	pending.album, pending.err = r.downloader.catalog.Album(ctx, r.storefront, albumID)
	r.mu.Lock()
	if pending.err != nil && r.albums[albumID] == pending {
		// Do not retain transient failures for the whole job. Concurrent callers
		// still share this result, while a later track may try the album again.
		delete(r.albums, albumID)
	}
	close(pending.done)
	r.mu.Unlock()
	return pending.album, pending.err
}

func mergeResolvedSong(song, initial applemusic.Song) applemusic.Song {
	song.ID = firstNonEmpty(song.ID, initial.ID)
	song.Name = firstNonEmpty(song.Name, initial.Name)
	song.ArtistName = firstNonEmpty(song.ArtistName, initial.ArtistName)
	song.AlbumName = firstNonEmpty(song.AlbumName, initial.AlbumName)
	song.ComposerName = firstNonEmpty(song.ComposerName, initial.ComposerName)
	if len(song.GenreNames) == 0 {
		song.GenreNames = append([]string(nil), initial.GenreNames...)
	}
	song.ReleaseDate = firstNonEmpty(song.ReleaseDate, initial.ReleaseDate)
	if song.TrackNumber == 0 {
		song.TrackNumber = initial.TrackNumber
	}
	if song.DiscNumber == 0 {
		song.DiscNumber = initial.DiscNumber
	}
	if song.DurationInMillis == 0 {
		song.DurationInMillis = initial.DurationInMillis
	}
	song.ISRC = firstNonEmpty(song.ISRC, initial.ISRC)
	song.ContentRating = firstNonEmpty(song.ContentRating, initial.ContentRating)
	song.ArtworkURL = firstNonEmpty(song.ArtworkURL, initial.ArtworkURL)
	song.ArtistArtworkURL = firstNonEmpty(song.ArtistArtworkURL, initial.ArtistArtworkURL)
	song.EnhancedHLS = firstNonEmpty(song.EnhancedHLS, initial.EnhancedHLS)
	song.AlbumID = firstNonEmpty(song.AlbumID, initial.AlbumID)
	song.ArtistID = firstNonEmpty(song.ArtistID, initial.ArtistID)

	// Collection resolves have more authoritative album-wide values than the
	// individual song relationship, especially disc and track totals.
	song.AlbumArtist = firstNonEmpty(initial.AlbumArtist, song.AlbumArtist)
	song.AlbumArtistID = firstNonEmpty(initial.AlbumArtistID, song.AlbumArtistID)
	song.AlbumArtworkURL = firstNonEmpty(initial.AlbumArtworkURL, song.AlbumArtworkURL)
	song.AlbumArtistArtworkURL = firstNonEmpty(initial.AlbumArtistArtworkURL, song.AlbumArtistArtworkURL)
	song.AlbumRelease = firstNonEmpty(initial.AlbumRelease, song.AlbumRelease)
	song.Copyright = firstNonEmpty(initial.Copyright, song.Copyright)
	song.RecordLabel = firstNonEmpty(initial.RecordLabel, song.RecordLabel)
	song.UPC = firstNonEmpty(initial.UPC, song.UPC)
	if initial.TrackCount > 0 {
		song.TrackCount = initial.TrackCount
	}
	if initial.DiscCount > 0 {
		song.DiscCount = initial.DiscCount
	}
	return song
}

func preserveCollectionTrackContext(song, initial applemusic.Song) applemusic.Song {
	song.AlbumID = firstNonEmpty(initial.AlbumID, song.AlbumID)
	song.AlbumName = firstNonEmpty(initial.AlbumName, song.AlbumName)
	song.ArtworkURL = firstNonEmpty(initial.ArtworkURL, song.ArtworkURL)
	if initial.TrackNumber > 0 {
		song.TrackNumber = initial.TrackNumber
	}
	if initial.DiscNumber > 0 {
		song.DiscNumber = initial.DiscNumber
	}
	return song
}

func enrichTrackWithAlbum(song applemusic.Song, album applemusic.Collection) applemusic.Song {
	song.AlbumArtworkURL = album.ArtworkURL
	song.AlbumArtistID = album.ArtistID
	song.AlbumArtistArtworkURL = album.ArtistArtworkURL
	for _, track := range album.Tracks {
		if track.ID == song.ID {
			song.TrackCount = track.TrackCount
			song.DiscCount = track.DiscCount
			break
		}
	}
	if song.TrackCount == 0 {
		song.TrackCount = len(album.Tracks)
	}
	if song.DiscCount == 0 {
		song.DiscCount = maxTrackDisc(album.Tracks)
	}
	song.AlbumArtist = album.Artist
	return song
}

func maxTrackDisc(tracks []applemusic.Song) int {
	maximum := 0
	for _, track := range tracks {
		if track.DiscNumber > maximum {
			maximum = track.DiscNumber
		}
	}
	if maximum == 0 {
		return 1
	}
	return maximum
}

func (d *Downloader) processTrack(ctx context.Context, job domain.Job, item domain.JobItem, initial applemusic.Song, storefront string, collectionType applemusic.URLType, collectionName, collectionID string, playlistIndex int, folderArtist string, reporter jobs.Reporter) error {
	return d.processTrackWithMetadata(ctx, job, item, initial, storefront, collectionType, collectionName, collectionID, playlistIndex, folderArtist, newTrackMetadataResolver(d, storefront), reporter)
}

func (d *Downloader) processTrackWithMetadata(ctx context.Context, job domain.Job, item domain.JobItem, initial applemusic.Song, storefront string, collectionType applemusic.URLType, collectionName, collectionID string, playlistIndex int, folderArtist string, metadata *trackMetadataResolver, reporter jobs.Reporter) error {
	// Once a codec's concrete output path is known, keep its process-wide lock
	// through the existence/force check, retries, sidecar writes, and final
	// commit. This prevents one job's force/cleanup path from deleting another
	// job's in-flight or newly committed output.
	var lockedOutputPath string
	var unlockOutput func()
	releaseOutput := func() {
		if unlockOutput != nil {
			unlockOutput()
			unlockOutput = nil
			lockedOutputPath = ""
		}
	}
	defer releaseOutput()
	acquireOutput := func(path string) error {
		key, err := canonicalOutputPath(path)
		if err != nil {
			return fmt.Errorf("canonicalize output path: %w", err)
		}
		if unlockOutput != nil && key == lockedOutputPath {
			return nil
		}
		releaseOutput()
		unlock, err := processOutputLocks.acquireContext(ctx, path)
		if err != nil {
			return err
		}
		lockedOutputPath = key
		unlockOutput = unlock
		return nil
	}

	// set updates item state and emits an item_progress SSE event.
	// The full JobItem is embedded in the event Payload so the frontend can
	// update the UI directly from SSE without any additional HTTP round-trips,
	// except artwork_url: cover art doesn't change during a download, so it's
	// stripped from the event to keep progress/status pushes lightweight — the
	// frontend already has it from the initial REST response.
	// To avoid flooding the stream — and hammering SQLite with one UPDATE per
	// 32KB download chunk — both the DB write and the event are gated on the
	// same threshold: status changed or progress moved by at least 1
	// percentage point. Intermediate progress has no durability value anyway
	// (unfinished items are reset to queued on resume); persisting it at 1pp
	// granularity only serves REST polling.
	var lastEmittedStatus domain.ItemStatus
	var lastEmittedProgress float64 = -1
	set := func(status domain.ItemStatus, progress float64, message string) {
		item.Status = status
		item.Progress = progress
		item.StatusMessage = message
		progPct := math.Round(progress * 100)
		lastPct := math.Round(lastEmittedProgress * 100)
		if status != lastEmittedStatus || progPct != lastPct {
			lastEmittedStatus = status
			lastEmittedProgress = progress
			_ = reporter.UpdateItem(ctx, item)
			eventItem := item
			eventItem.ArtworkURL = ""
			_ = reporter.Event(ctx, domain.Event{
				JobID:   job.ID,
				ItemID:  item.ID,
				Type:    "item_progress",
				Phase:   string(status),
				Message: message,
				Payload: marshalPayload(eventItem), // full item state for frontend, minus cover art
			})
		}
	}

	set(domain.ItemResolving, 0.01, "resolving metadata")

	song, metadataAttempts, err := retryValue(ctx, d.cfg.Download.MaxAttempts, retryBackoff, func(attempt int) (applemusic.Song, error) {
		d.setItemAttempt(ctx, reporter, &item, "metadata", attempt, clampAttempts(d.cfg.Download.MaxAttempts), fmt.Sprintf("Fetching track metadata (%d/%d)", attempt, clampAttempts(d.cfg.Download.MaxAttempts)))
		return metadata.song(ctx, initial, collectionType)
	}, func(failure retryFailure) {
		d.setRetryFailure(ctx, reporter, &item, "metadata", "metadata", failure)
		d.emitRetryEvent(ctx, reporter, job.ID, item.ID, "metadata", "", failure)
	})
	if err != nil {
		return d.failItem(ctx, reporter, job, item, err)
	}
	if metadataAttempts > 1 {
		d.emitRecoveredEvent(ctx, reporter, job.ID, item.ID, "metadata", "", metadataAttempts)
	}
	item.Title = song.Name
	item.Artist = song.ArtistName
	item.Album = song.AlbumName
	item.ArtworkURL = firstNonEmpty(song.ArtworkURL, song.AlbumArtworkURL, item.ArtworkURL)
	item.HasLyrics = song.HasLyrics
	_ = reporter.UpdateItem(ctx, item)

	if d.cfg.Simulate.Enabled {
		// Test mode: real catalog metadata was resolved above, but everything
		// from here on (covers, lyrics, media selection, transfer, decrypt,
		// disk writes) is simulated with an identical status/event lifecycle.
		return d.simulateTrack(ctx, job, &item, song, collectionType, collectionName, collectionID, playlistIndex, folderArtist, reporter, set)
	}

	albumCoverDir, artistCoverDir := standaloneCoverDirs(d.cfg, song, collectionType, playlistIndex, folderArtist, collectionName, collectionID)
	if d.cfg.Download.SaveAlbumCover || d.cfg.Download.SaveArtistCover {
		if coverErr := d.saveStandaloneCovers(ctx, song, collectionType, storefront, albumCoverDir, artistCoverDir); coverErr != nil {
			item.StatusMessage = "Standalone cover save failed; continuing download: " + coverErr.Error()
			_ = reporter.UpdateItem(ctx, item)
			_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "standalone_cover_failed", Message: coverErr.Error()})
		}
	}
	var cover []byte
	if d.cfg.Download.EmbedCover {
		coverURLs := trackCoverURLs(song, collectionType)
		var coverAttempts int
		cover, coverAttempts, err = retryValue(ctx, d.cfg.Download.MaxAttempts, retryBackoff, func(attempt int) ([]byte, error) {
			d.setItemAttempt(ctx, reporter, &item, "cover", attempt, clampAttempts(d.cfg.Download.MaxAttempts), fmt.Sprintf("Fetching cover (%d/%d)", attempt, clampAttempts(d.cfg.Download.MaxAttempts)))
			return d.fetchCover(ctx, coverURLs, d.cfg.Download.CoverFormat, d.cfg.Download.CoverSize)
		}, func(failure retryFailure) {
			d.setRetryFailure(ctx, reporter, &item, "cover", "cover", failure)
			d.emitRetryEvent(ctx, reporter, job.ID, item.ID, "cover", "", failure)
		})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			item.StatusMessage = "Cover fetch retries exhausted; continuing without embedded cover: " + err.Error()
			_ = reporter.UpdateItem(ctx, item)
		} else if coverAttempts > 1 {
			d.emitRecoveredEvent(ctx, reporter, job.ID, item.ID, "cover", "", coverAttempts)
		}
	}
	lyrics := ""
	if (d.cfg.Download.EmbedLyrics || d.cfg.Download.SaveLyricsFile) && song.HasLyrics {
		raw, lyricsAttempts, lyricsErr := retryValue(ctx, d.cfg.Download.MaxAttempts, retryBackoff, func(attempt int) (string, error) {
			d.setItemAttempt(ctx, reporter, &item, "lyrics", attempt, clampAttempts(d.cfg.Download.MaxAttempts), fmt.Sprintf("Fetching lyrics (%d/%d)", attempt, clampAttempts(d.cfg.Download.MaxAttempts)))
			return d.wrapper.Lyrics(ctx, song.ID, wrapper.LyricsRequestOptions{
				Region:                  storefront,
				Language:                d.cfg.Catalog.Language,
				Type:                    d.cfg.Download.LyricsType,
				ExtendTtmlLocalizations: len(d.cfg.Download.LyricsExtras) > 0 || d.cfg.Download.LyricsType == "syllable-lyrics",
			})
		}, func(failure retryFailure) {
			d.setRetryFailure(ctx, reporter, &item, "lyrics", "lyrics", failure)
			d.emitRetryEvent(ctx, reporter, job.ID, item.ID, "lyrics", "", failure)
		})
		if lyricsErr == nil {
			converted, convertErr := convertLyrics(raw, d.cfg.Download.LyricsFormat, d.cfg.Download.LyricsExtras)
			switch {
			case convertErr != nil:
				item.LyricsStatus = domain.LyricsFailed
				item.StatusMessage = "Lyrics conversion failed; continuing without embedded lyrics: " + convertErr.Error()
				_ = reporter.UpdateItem(ctx, item)
			case converted == "":
				// The wrapper answered with an empty document (possible in ttml
				// mode, where convertLyrics passes it through without error).
				item.LyricsStatus = domain.LyricsFailed
				item.StatusMessage = "Lyrics fetch returned an empty document; continuing without embedded lyrics"
				_ = reporter.UpdateItem(ctx, item)
			default:
				lyrics = converted
				item.LyricsStatus = domain.LyricsFetched
				_ = reporter.UpdateItem(ctx, item)
				if lyricsAttempts > 1 {
					d.emitRecoveredEvent(ctx, reporter, job.ID, item.ID, "lyrics", "", lyricsAttempts)
				}
			}
		} else {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			item.LyricsStatus = domain.LyricsFailed
			item.StatusMessage = "Lyrics fetch retries exhausted; continuing without embedded lyrics: " + lyricsErr.Error()
			_ = reporter.UpdateItem(ctx, item)
		}
	} else {
		if song.HasLyrics {
			item.LyricsStatus = domain.LyricsDisabled
		} else {
			item.LyricsStatus = domain.LyricsNone
		}
		_ = reporter.UpdateItem(ctx, item)
	}

	if err := d.tools.Require(ctx); err != nil {
		return d.failItem(ctx, reporter, job, item, err)
	}
	codecs, err := configuredCodecs(d.cfg.Download)
	if err != nil {
		return d.failItem(ctx, reporter, job, item, err)
	}
	var lastErr error
	for codecIndex, codec := range codecs {
		codecMaxAttempts := attemptsForCodec(d.cfg.Download.MaxAttempts, codecIndex)
		if codecIndex > 0 {
			item.StatusMessage = fmt.Sprintf("Codec %s failed; falling back to %s", strings.ToUpper(codecs[codecIndex-1]), strings.ToUpper(codec))
			_ = reporter.UpdateItem(ctx, item)
			_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "codec_fallback", Phase: codec, Message: item.StatusMessage, Payload: marshalPayload(map[string]any{
				"from_codec": codecs[codecIndex-1], "to_codec": codec, "reason": codecFailureReason(lastErr),
			})})
		}
		item.Codec = codec
		// Cleared so a client reading the snapshot mid-fallback never sees the
		// previous codec's quality paired with the new codec's name; setItemQuality
		// repopulates them once the new attempt's actual quality is known.
		item.BitDepth, item.SampleRate, item.Bitrate = 0, 0, 0
		attemptOutPath := ""
		skipped := false

		// Fetch phase: acquire the still-encrypted media into the configured memory
		// or disk backing. Retried on its own so a later decrypt failure doesn't
		// force a redundant re-download of bytes already fetched successfully.
		var aacMedia aacLCMedia
		var enhanced selectedDownloadMedia
		var rawAACLC []byte
		_, fetchAttempts, fetchErr := retryValue(ctx, codecMaxAttempts, retryBackoff, func(attempt int) (struct{}, error) {
			if codec == "aac-lc" {
				d.setItemAttempt(ctx, reporter, &item, "download", attempt, clampAttempts(codecMaxAttempts), fmt.Sprintf("Downloading %s (%d/%d)", strings.ToUpper(codec), attempt, clampAttempts(codecMaxAttempts)))
				attemptOutPath = outputPath(d.cfg, song, collectionType, playlistIndex, folderArtist, collectionName, collectionID, codec, "256Kbps")
				if err := acquireOutput(attemptOutPath); err != nil {
					return struct{}{}, err
				}
				// No per-track manifest to read quality from for aac-lc (unlike the
				// enhanced codecs' HLS variant); leave bit_depth/sample_rate/bitrate
				// at 0 (omitted from the API response) rather than assert a value.
				if skip, err := d.handleExistingOutput(ctx, reporter, job, &item, attemptOutPath); skip || err != nil {
					skipped = skip
					return struct{}{}, err
				}
				media, raw, err := d.fetchAACLCMedia(ctx, job, &item, song, reporter, set)
				if err != nil {
					return struct{}{}, err
				}
				aacMedia, rawAACLC = media, raw
				return struct{}{}, nil
			}
			d.setItemAttempt(ctx, reporter, &item, "download", attempt, clampAttempts(codecMaxAttempts), fmt.Sprintf("Selecting %s (%d/%d)", strings.ToUpper(codec), attempt, clampAttempts(codecMaxAttempts)))
			selected, selectErr := d.selectEnhancedMedia(ctx, job, &item, song, codec, reporter, set)
			if selectErr != nil {
				return struct{}{}, selectErr
			}
			attemptOutPath = outputPath(d.cfg, song, collectionType, playlistIndex, folderArtist, collectionName, collectionID, codec, qualityLabel(selected.info))
			if err := acquireOutput(attemptOutPath); err != nil {
				return struct{}{}, err
			}
			if skip, err := d.handleExistingOutput(ctx, reporter, job, &item, attemptOutPath); skip || err != nil {
				skipped = skip
				return struct{}{}, err
			}
			selected, downloadErr := d.downloadSelectedEnhancedMedia(ctx, selected, codec, set)
			if downloadErr != nil {
				return struct{}{}, downloadErr
			}
			enhanced = selected
			return struct{}{}, nil
		}, func(failure retryFailure) {
			if unlockOutput != nil && attemptOutPath != "" {
				cleanupFailedOutput(attemptOutPath)
			}
			operation := strings.ToUpper(codec)
			if isNonRetryableError(failure.Err) {
				operation = "select " + operation
			}
			d.setRetryFailure(ctx, reporter, &item, "download", operation, failure)
			d.emitRetryEvent(ctx, reporter, job.ID, item.ID, "download", codec, failure)
		})
		if skipped && fetchErr == nil {
			releaseOutput()
			return nil
		}
		if fetchErr != nil {
			releaseOutput()
			lastErr = fetchErr
			d.reportCodecFailed(ctx, reporter, job, item, codec, "download", codecMaxAttempts, fetchAttempts, fetchErr)
			continue
		}
		if fetchAttempts > 1 {
			d.emitRecoveredEvent(ctx, reporter, job.ID, item.ID, "download", codec, fetchAttempts)
		}

		// Decrypt phase: extract/decrypt/remux/tag the already-downloaded bytes.
		// Retried independently of the fetch phase — a decrypt failure re-runs
		// only this closure, reusing the encrypted bytes fetched above instead
		// of hitting Apple's CDN again.
		_, decryptAttempts, decryptErr := retryValue(ctx, codecMaxAttempts, retryBackoff, func(attempt int) (struct{}, error) {
			d.setItemAttempt(ctx, reporter, &item, "decrypt", attempt, clampAttempts(codecMaxAttempts), fmt.Sprintf("Decrypting %s (%d/%d)", strings.ToUpper(codec), attempt, clampAttempts(codecMaxAttempts)))
			if codec == "aac-lc" {
				return struct{}{}, d.decryptAACLC(ctx, &item, song, aacMedia, rawAACLC, lyrics, cover, attemptOutPath, set)
			}
			return struct{}{}, d.downloadEnhancedCodec(ctx, job, &item, song, codec, lyrics, cover, attemptOutPath, enhanced, reporter, set)
		}, func(failure retryFailure) {
			if unlockOutput != nil && attemptOutPath != "" {
				cleanupFailedOutput(attemptOutPath)
			}
			d.setRetryFailure(ctx, reporter, &item, "decrypt", strings.ToUpper(codec), failure)
			d.emitRetryEvent(ctx, reporter, job.ID, item.ID, "decrypt", codec, failure)
		})
		// Low-memory mode's encrypted scratch file is no longer needed once the
		// decrypt phase has run (whether it succeeded or exhausted its retries); a
		// fallback codec fetches its own. High-memory mode and AAC-LC have an empty
		// rawPath, so this is a no-op and their byte slices become collectible when
		// the current codec scope is replaced or returns.
		cleanupTempFile(enhanced.rawPath)
		if decryptErr != nil {
			releaseOutput()
			lastErr = decryptErr
			d.reportCodecFailed(ctx, reporter, job, item, codec, "decrypt", codecMaxAttempts, decryptAttempts, decryptErr)
			continue
		}
		if decryptAttempts > 1 {
			d.emitRecoveredEvent(ctx, reporter, job.ID, item.ID, "decrypt", codec, decryptAttempts)
		}

		codecName := strings.ToUpper(codec)
		switch {
		case codecIndex > 0:
			item.StatusMessage = fmt.Sprintf("Completed after fallback to %s", codecName)
		case fetchAttempts > 1 && decryptAttempts > 1:
			item.StatusMessage = fmt.Sprintf("%s completed (download took %d attempts, decrypt took %d attempts)", codecName, fetchAttempts, decryptAttempts)
		case fetchAttempts > 1:
			item.StatusMessage = fmt.Sprintf("%s completed (download took %d attempts)", codecName, fetchAttempts)
		case decryptAttempts > 1:
			item.StatusMessage = fmt.Sprintf("%s completed (decrypt took %d attempts)", codecName, decryptAttempts)
		default:
			item.StatusMessage = fmt.Sprintf("%s download completed", codecName)
		}
		_ = reporter.UpdateItem(ctx, item)
		_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "item_completed", Message: item.StatusMessage, Payload: marshalPayload(map[string]any{
			"codec": codec, "download_attempts": fetchAttempts, "decrypt_attempts": decryptAttempts,
			"max_attempts": clampAttempts(codecMaxAttempts), "fallback_from": fallbackCodec(codecs, codecIndex),
		})})
		releaseOutput()
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no configured codec succeeded")
	}
	return d.failItem(ctx, reporter, job, item, lastErr)
}

// reportCodecFailed emits the codec_failed event for either the download or
// the decrypt phase, using the attempts actually spent in that phase.
func (d *Downloader) reportCodecFailed(ctx context.Context, reporter jobs.Reporter, job domain.Job, item domain.JobItem, codec, phase string, codecMaxAttempts, attempts int, err error) {
	attemptMaximum := clampAttempts(codecMaxAttempts)
	if isNonRetryableError(err) {
		attemptMaximum = attempts
	}
	_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "codec_failed", Phase: codec, Message: err.Error(), Payload: marshalPayload(map[string]any{
		"codec": codec, "phase": phase, "attempts": attempts, "max_attempts": attemptMaximum, "error": err.Error(),
	})})
}

func configuredCodec(value string) (string, error) {
	codec := strings.ToLower(strings.TrimSpace(value))
	if codec == "" {
		return "", fmt.Errorf("codec in quality_priority cannot be empty")
	}
	switch codec {
	case "alac", "aac", "aac-binaural", "aac-downmix", "ec3", "ac3", "aac-lc":
		return codec, nil
	default:
		return "", fmt.Errorf("unsupported configured codec %q", value)
	}
}

func configuredCodecs(cfg config.DownloadConfig) ([]string, error) {
	values := append([]string(nil), cfg.QualityPriority...)
	if len(values) == 0 {
		return nil, fmt.Errorf("quality_priority must contain at least one codec")
	}
	if !cfg.CodecAlternative && len(values) > 1 {
		values = values[:1]
	}
	if cfg.CodecAlternative {
		values = append(values, "aac-lc")
	}
	codecs := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		codec, err := configuredCodec(value)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[codec]; exists {
			continue
		}
		seen[codec] = struct{}{}
		codecs = append(codecs, codec)
	}
	if len(codecs) == 0 {
		return nil, fmt.Errorf("quality_priority must contain at least one codec")
	}
	return codecs, nil
}

func fallbackCodec(codecs []string, selectedIndex int) string {
	if selectedIndex > 0 && len(codecs) > 0 {
		return codecs[0]
	}
	return ""
}

func codecFailureReason(err error) string {
	if err == nil {
		return "previous codec failed"
	}
	return err.Error()
}

func attemptsForCodec(configuredMaxAttempts, _ int) int {
	return configuredMaxAttempts
}

func (d *Downloader) handleExistingOutput(ctx context.Context, reporter jobs.Reporter, job domain.Job, item *domain.JobItem, outPath string) (bool, error) {
	item.OutputPath = outPath
	if _, err := os.Stat(outPath); err == nil && !job.Force {
		item.Status = domain.ItemSkipped
		item.Progress = 1
		item.RetryKind = ""
		item.Attempt = 0
		item.MaxAttempts = 0
		item.StatusMessage = "File already exists; skipped"
		_ = reporter.UpdateItem(ctx, *item)
		_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "item_skipped", Message: "already exists"})
		return true, nil
	}
	if job.Force {
		cleanupFailedOutput(outPath)
		_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "item_overwrite", Message: "force overwrite enabled"})
	}
	return false, nil
}

func (d *Downloader) selectEnhancedMedia(ctx context.Context, job domain.Job, item *domain.JobItem, song applemusic.Song, codec string, reporter jobs.Reporter, set func(domain.ItemStatus, float64, string)) (selectedDownloadMedia, error) {
	set(domain.ItemDownloading, 0.03, fmt.Sprintf("Selecting %s media stream", strings.ToUpper(codec)))
	master := song.EnhancedHLS
	if d.cfg.Catalog.DeveloperTokenSigningEnabled() {
		// A self-signed developer token cannot read enhancedHls, so the master
		// playlist comes from either the wrapper's authorized device manifest
		// or a scraped web-player token, per catalog.signed_mode_hls_source.
		if d.cfg.Catalog.EnhancedHLSFromWebToken() {
			hls, err := d.catalog.EnhancedHLSViaWebToken(ctx, job.Storefront, song.ID)
			if err != nil {
				return selectedDownloadMedia{}, fmt.Errorf("fetch enhanced hls via web token: %w", err)
			}
			master = hls
		} else {
			m3u8, err := d.wrapper.M3U8(ctx, song.ID)
			if err != nil {
				return selectedDownloadMedia{}, fmt.Errorf("request device m3u8: %w", err)
			}
			master = m3u8
		}
	}
	if master == "" {
		return selectedDownloadMedia{}, fmt.Errorf("no enhanced hls manifest")
	}
	info, err := extractMedia(ctx, d.http, master, codec, d.cfg.Download.ALACMaxSampleRate, d.cfg.Download.ALACMaxBitDepth)
	if err != nil {
		return selectedDownloadMedia{}, fmt.Errorf("select %s media: %w", codec, err)
	}
	d.setItemQuality(ctx, reporter, item, info.BitDepth, info.SampleRate, info.Bandwidth)
	payload, _ := json.Marshal(map[string]any{"codec_id": info.CodecID, "bit_depth": info.BitDepth, "sample_rate": info.SampleRate, "attempt": item.Attempt, "max_attempts": item.MaxAttempts})
	_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "codec_selected", Phase: codec, Payload: string(payload)})

	return selectedDownloadMedia{info: info}, nil
}

func (d *Downloader) downloadSelectedEnhancedMedia(ctx context.Context, selected selectedDownloadMedia, codec string, set func(domain.ItemStatus, float64, string)) (selectedDownloadMedia, error) {
	codecName := strings.ToUpper(codec)
	set(domain.ItemDownloading, 0.05, fmt.Sprintf("Downloading %s encrypted media", codecName))
	onProgress := func(p float64) {
		if p < 0 {
			return // Content-Length unknown, stay at 5%
		}
		// map [0,1] → [0.05, 0.55]
		set(domain.ItemDownloading, 0.05+p*0.50, fmt.Sprintf("%s download %.0f%%", codecName, p*100))
	}
	release, err := acquireOperationSlot(ctx, d.mediaLimiter)
	if err != nil {
		return selectedDownloadMedia{}, err
	}
	defer release()
	if d.cfg.Download.MemoryMode == config.MemoryModeHigh {
		// High-memory mode keeps exactly one whole-track encrypted copy. The
		// fragment decrypt/remux stage remains streaming, so parsed, plaintext,
		// and remuxed whole-track copies never accumulate beside it.
		raw, err := downloadBytes(ctx, d.http, selected.info.MediaURI, onProgress)
		if err != nil {
			return selectedDownloadMedia{}, fmt.Errorf("download encrypted media: %w", err)
		}
		selected.raw = raw
		return selected, nil
	}

	// Low-memory mode streams the encrypted response to scratch and reads it back
	// fragment-by-fragment during decrypt.
	rawPath, err := downloadToFile(ctx, d.http, selected.info.MediaURI, d.cfg.Download.TempDir, onProgress)
	if err != nil {
		return selectedDownloadMedia{}, fmt.Errorf("download encrypted media: %w", err)
	}
	selected.rawPath = rawPath
	return selected, nil
}

func (d *Downloader) downloadEnhancedCodec(ctx context.Context, job domain.Job, item *domain.JobItem, song applemusic.Song, codec, lyrics string, cover []byte, outPath string, selected selectedDownloadMedia, reporter jobs.Reporter, set func(domain.ItemStatus, float64, string)) error {
	info := selected.info
	var (
		rawReader io.Reader
		rawSize   int64
		rawFile   *os.File
		err       error
	)
	if d.cfg.Download.MemoryMode == config.MemoryModeHigh {
		rawReader = bytes.NewReader(selected.raw)
		rawSize = int64(len(selected.raw))
	} else {
		rawFile, err = os.Open(selected.rawPath)
		if err != nil {
			return fmt.Errorf("open downloaded media: %w", err)
		}
		defer rawFile.Close()
		rawReader = rawFile
		if fi, statErr := rawFile.Stat(); statErr == nil {
			rawSize = fi.Size()
		}
	}

	// Both modes stage the flattened, verified and tagged output here before the
	// atomic final move. Low mode first writes a dec-* fragmented intermediate;
	// high mode pipes that same fragment stream straight into ffmpeg.
	flatFile, err := os.CreateTemp(d.cfg.Download.TempDir, "flat-*.m4a")
	if err != nil {
		return fmt.Errorf("create flatten output: %w", err)
	}
	flatPath := flatFile.Name()
	flatFile.Close()
	defer os.Remove(flatPath)

	session, err := d.wrapper.NewDecryptSession(ctx, song.ID)
	if err != nil {
		return fmt.Errorf("open decrypt session: %w", err)
	}
	set(domain.ItemDecrypting, 0.55, "decrypting")
	// Progress tracks encrypted bytes consumed (55% → 90%); the total sample
	// count isn't known until the last fragment is read.
	decryptFragment := func(key string, samples [][]byte) ([][]byte, error) {
		return session.DecryptFragment(key, samples)
	}
	onProgress := func(consumed uint64) {
		if rawSize <= 0 {
			return
		}
		p := float64(consumed) / float64(rawSize)
		if p > 1 {
			p = 1
		}
		set(domain.ItemDecrypting, 0.55+p*0.35, fmt.Sprintf("decrypting %.0f%%", p*100))
	}

	if d.cfg.Download.MemoryMode == config.MemoryModeHigh {
		// The encrypted track is the only whole-track allocation. mp4ff parses one
		// fragment, the wrapper returns one plaintext fragment, and DataParts writes
		// those samples directly into ffmpeg's stdin without another concatenation.
		streamErr := d.mp4.streamDecryptToFlatFile(ctx, rawReader, flatPath, info.Keys, decryptFragment, onProgress)
		closeErr := session.Close()
		if streamErr != nil {
			return fmt.Errorf("decrypt and flatten media: %w", streamErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close decrypt session: %w", closeErr)
		}
		// Flattening already ran behind the decrypt stream, but preserve the public
		// lifecycle transition before verification/saving.
		set(domain.ItemRemuxing, 0.90, "remuxing")
	} else {
		decFile, createErr := os.CreateTemp(d.cfg.Download.TempDir, "dec-*.mp4")
		if createErr != nil {
			_ = session.Close()
			return fmt.Errorf("create decrypt output: %w", createErr)
		}
		decPath := decFile.Name()
		decFile.Close()
		defer os.Remove(decPath)
		streamErr := d.mp4.streamDecryptToFile(ctx, rawReader, decPath, info.Keys,
			decryptFragment, onProgress)
		closeErr := session.Close()
		if streamErr != nil {
			return fmt.Errorf("decrypt media: %w", streamErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close decrypt session: %w", closeErr)
		}
		set(domain.ItemRemuxing, 0.90, "remuxing")
		if err := d.mp4.flattenFileToFile(ctx, decPath, flatPath); err != nil {
			return fmt.Errorf("fix encapsulation: %w", err)
		}
	}
	// Flatten the decrypted fragmented MP4 into a regular progressive MP4 (also
	// normalises the ftyp brand) on temp storage, then verify and tag it there,
	// and only move the finished file to its final path. Keeping the flatten
	// write, the integrity read-back, and the tag rewrite on temp (typically
	// fast local disk) means a possibly-slow downloads volume sees just one
	// sequential write; see finalizeToOutput. The decoder configuration is
	// carried over from the original init segment, so no esds fixup is needed.
	set(domain.ItemSaving, 0.94, "saving")
	if d.cfg.Download.CheckIntegrity && !d.mp4.checkIntegrityFile(ctx, flatPath) {
		if codec != "alac" {
			return fmt.Errorf("integrity check failed")
		}
		if err := d.repairALACFile(ctx, job, item, flatPath, codec, reporter); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	if d.cfg.Download.SaveLyricsFile && lyrics != "" {
		ext := ".lrc"
		if d.cfg.Download.LyricsFormat == "ttml" {
			ext = ".ttml"
		}
		if err := os.WriteFile(strings.TrimSuffix(outPath, ".m4a")+ext, []byte(lyrics), 0o644); err != nil {
			return fmt.Errorf("write lyrics file: %w", err)
		}
	}
	set(domain.ItemTagging, 0.97, "writing metadata")
	if err := d.mp4.writeMetadata(ctx, flatPath, song, lyrics, cover, songInfo{Codec: codec}); err != nil {
		return fmt.Errorf("write metadata: %w", err)
	}
	// Only ever expose a complete, tagged file at outPath: finalizeToOutput
	// renames (same filesystem) or copies through a .part then renames
	// (cross-filesystem), so handleExistingOutput's "bare existence = done" skip
	// stays correct even if the process dies mid-move.
	if err := finalizeToOutput(flatPath, outPath); err != nil {
		return fmt.Errorf("finalize output file: %w", err)
	}
	item.Status = domain.ItemCompleted
	item.Progress = 1
	item.OutputPath = outPath
	item.Codec = codec
	return nil
}

// repairALACFile is the ALAC integrity-repair fallback, run only when the
// flattened file fails the decode check. It reads the file back, patches
// malformed ALAC packet terminators, re-verifies, and writes the repaired bytes
// in place — keeping the whole-file read off the common (already-valid) path.
func (d *Downloader) repairALACFile(ctx context.Context, job domain.Job, item *domain.JobItem, path, codec string, reporter jobs.Reporter) error {
	outBytes, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("integrity check failed; read for alac repair: %w", err)
	}
	repaired, patched, err := repairALACTerminators(outBytes)
	if err != nil {
		return fmt.Errorf("integrity check failed; alac repair failed: %w", err)
	}
	if patched == 0 {
		return fmt.Errorf("integrity check failed; alac repair found no malformed packets")
	}
	payload, _ := json.Marshal(map[string]any{"patched_packets": patched})
	_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "alac_repaired", Phase: codec, Payload: string(payload)})
	if err := os.WriteFile(path, repaired, 0o644); err != nil {
		return fmt.Errorf("integrity check failed; write repaired alac: %w", err)
	}
	if !d.mp4.checkIntegrityFile(ctx, path) {
		return fmt.Errorf("integrity check failed after alac repair")
	}
	return nil
}

// fetchAACLCMedia resolves the AAC-LC media playlist and downloads the
// still-encrypted stream into memory. Kept separate from decryptAACLC so a
// decrypt-phase retry can reuse these bytes instead of re-hitting the CDN.
func (d *Downloader) fetchAACLCMedia(ctx context.Context, job domain.Job, item *domain.JobItem, song applemusic.Song, reporter jobs.Reporter, set func(domain.ItemStatus, float64, string)) (aacLCMedia, []byte, error) {
	set(domain.ItemDownloading, 0.03, "requesting AAC-LC WebPlayback asset")
	playlistURL, err := d.wrapper.WebPlayback(ctx, song.ID)
	if err != nil {
		return aacLCMedia{}, nil, fmt.Errorf("request AAC-LC WebPlayback: %w", err)
	}
	media, err := extractAACLCMedia(ctx, d.http, playlistURL)
	if err != nil {
		return aacLCMedia{}, nil, fmt.Errorf("parse AAC-LC media playlist: %w", err)
	}
	_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "codec_selected", Phase: "aac-lc", Payload: marshalPayload(map[string]any{
		"codec_id": "aac-lc", "attempt": item.Attempt, "max_attempts": item.MaxAttempts,
	})})

	set(domain.ItemDownloading, 0.05, "downloading encrypted AAC-LC media")
	release, err := acquireOperationSlot(ctx, d.mediaLimiter)
	if err != nil {
		return aacLCMedia{}, nil, err
	}
	defer release()
	// Stream-download with per-chunk progress from 5% → 55%
	raw, err := downloadBytes(ctx, d.http, media.MediaURI, func(p float64) {
		if p < 0 {
			return
		}
		// map [0,1] → [0.05, 0.55]
		set(domain.ItemDownloading, 0.05+p*0.50, fmt.Sprintf("downloading %.0f%%", p*100))
	})
	if err != nil {
		return aacLCMedia{}, nil, fmt.Errorf("download encrypted AAC-LC media: %w", err)
	}
	return media, raw, nil
}

// decryptAACLC takes the already-downloaded encrypted bytes from
// fetchAACLCMedia and acquires the license, decrypts, and writes the final
// file. On retry this re-runs without downloading media again.
func (d *Downloader) decryptAACLC(ctx context.Context, item *domain.JobItem, song applemusic.Song, media aacLCMedia, raw []byte, lyrics string, cover []byte, outPath string, set func(domain.ItemStatus, float64, string)) error {
	set(domain.ItemDecrypting, 0.55, "acquiring Widevine license")
	challenge, parseLicense, err := newWidevineSession(media.KID)
	if err != nil {
		return err
	}
	license, err := d.wrapper.License(ctx, song.ID, base64.StdEncoding.EncodeToString(challenge), media.KeyURI)
	if err != nil {
		return fmt.Errorf("request AAC-LC license: %w", err)
	}
	decrypted, err := decryptWidevineMP4(raw, license, parseLicense)
	if err != nil {
		return err
	}

	set(domain.ItemSaving, 0.94, "saving AAC-LC")
	// Flatten, verify, and tag on temp storage, then move the finished file to
	// its final path, same as downloadEnhancedCodec (see finalizeToOutput).
	//
	// Normalise the container (flatten + M4A brand + create the
	// moov.udta.meta.ilst structure that go-mp4tag writes into). The decrypted
	// WebPlayback asset has no udta box, so tagging would otherwise fail. The
	// flattened bytes are not read back into memory.
	flatFile, err := os.CreateTemp(d.cfg.Download.TempDir, "flat-*.m4a")
	if err != nil {
		return fmt.Errorf("create flatten output: %w", err)
	}
	flatPath := flatFile.Name()
	flatFile.Close()
	defer os.Remove(flatPath)
	if err := d.mp4.flattenToFile(ctx, decrypted, flatPath); err != nil {
		return fmt.Errorf("normalize AAC-LC container: %w", err)
	}
	decrypted = nil
	if d.cfg.Download.CheckIntegrity && !d.mp4.checkIntegrityFile(ctx, flatPath) {
		return fmt.Errorf("AAC-LC integrity check failed")
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	if d.cfg.Download.SaveLyricsFile && lyrics != "" {
		ext := ".lrc"
		if d.cfg.Download.LyricsFormat == "ttml" {
			ext = ".ttml"
		}
		if err := os.WriteFile(strings.TrimSuffix(outPath, ".m4a")+ext, []byte(lyrics), 0o644); err != nil {
			return fmt.Errorf("write lyrics file: %w", err)
		}
	}
	set(domain.ItemTagging, 0.97, "writing AAC-LC metadata")
	if err := d.mp4.writeMetadata(ctx, flatPath, song, lyrics, cover, songInfo{Codec: "aac-lc"}); err != nil {
		return fmt.Errorf("write AAC-LC metadata: %w", err)
	}
	if err := finalizeToOutput(flatPath, outPath); err != nil {
		return fmt.Errorf("finalize AAC-LC output file: %w", err)
	}
	item.Status = domain.ItemCompleted
	item.Progress = 1
	item.OutputPath = outPath
	item.Codec = "aac-lc"
	return nil
}

func (d *Downloader) setItemAttempt(ctx context.Context, reporter jobs.Reporter, item *domain.JobItem, kind string, attempt, maximum int, message string) {
	item.RetryKind = kind
	item.Attempt = attempt
	item.MaxAttempts = maximum
	item.StatusMessage = message
	_ = reporter.UpdateItem(ctx, *item)
}

// setItemQuality persists the concrete audio quality of the codec currently
// being attempted once it's known, so a client reading the job snapshot
// mid-download sees the same bit depth/sample rate/bitrate that will end up
// in the final file rather than just the codec name.
func (d *Downloader) setItemQuality(ctx context.Context, reporter jobs.Reporter, item *domain.JobItem, bitDepth, sampleRate, bitrate int) {
	item.BitDepth = bitDepth
	item.SampleRate = sampleRate
	item.Bitrate = bitrate
	_ = reporter.UpdateItem(ctx, *item)
}

func (d *Downloader) setRetryFailure(ctx context.Context, reporter jobs.Reporter, item *domain.JobItem, kind, operation string, failure retryFailure) {
	item.RetryKind = kind
	item.Attempt = failure.Attempt
	item.MaxAttempts = failure.MaxAttempts
	if failure.WillRetry {
		item.StatusMessage = fmt.Sprintf("%s attempt %d/%d failed; retrying in %s: %v", operation, failure.Attempt, failure.MaxAttempts, failure.Delay, failure.Err)
	} else {
		item.StatusMessage = fmt.Sprintf("%s failed after %d attempt(s): %v", operation, failure.Attempt, failure.Err)
	}
	_ = reporter.UpdateItem(ctx, *item)
}

func (d *Downloader) emitRetryEvent(ctx context.Context, reporter jobs.Reporter, jobID, itemID, operation, codec string, failure retryFailure) {
	eventType := "operation_exhausted"
	if operation == "codec" {
		eventType = "codec_exhausted"
	}
	if failure.WillRetry {
		eventType = "operation_retry"
		if operation == "codec" {
			eventType = "codec_retry"
		}
	}
	message := fmt.Sprintf("%s attempt %d/%d failed: %v", operation, failure.Attempt, failure.MaxAttempts, failure.Err)
	_ = reporter.Event(ctx, domain.Event{JobID: jobID, ItemID: itemID, Type: eventType, Phase: firstNonEmpty(codec, operation), Message: message, Payload: marshalPayload(map[string]any{
		"operation": operation, "codec": codec, "attempt": failure.Attempt, "max_attempts": failure.MaxAttempts,
		"will_retry": failure.WillRetry, "delay_ms": failure.Delay.Milliseconds(), "error": failure.Err.Error(),
	})})
}

func (d *Downloader) emitRecoveredEvent(ctx context.Context, reporter jobs.Reporter, jobID, itemID, operation, codec string, attempt int) {
	eventType := "operation_recovered"
	if operation == "codec" {
		eventType = "codec_recovered"
	}
	_ = reporter.Event(ctx, domain.Event{JobID: jobID, ItemID: itemID, Type: eventType, Phase: firstNonEmpty(codec, operation), Message: fmt.Sprintf("%s succeeded on attempt %d", operation, attempt), Payload: marshalPayload(map[string]any{
		"operation": operation, "codec": codec, "attempt": attempt,
	})})
}

func marshalPayload(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

// partSuffix marks an output file that is still being written/tagged. The
// finalize step renames it onto the bare outPath only after metadata is in,
// so existence at the final path always implies a complete, tagged file.
const partSuffix = ".part"

// cleanupTempFile removes a temp file created during processing, tolerating an
// empty path (e.g. the aac-lc codec, which keeps its media in memory).
func cleanupTempFile(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}

// finalizeToOutput moves the finished, tagged file at src (staged on temp
// storage) to its final path dst. When src and dst share a filesystem this is a
// cheap atomic rename; when they don't (e.g. temp on SSD, downloads on HDD),
// os.Rename fails with EXDEV and it falls back to copying through a dst-side
// .part file that is then renamed into place. Either way dst only ever appears
// as a complete file, so handleExistingOutput's "bare existence = done" skip
// stays correct even if the process dies mid-move. On the copy path src is left
// in place for the caller's temp cleanup to remove.
//
// The finished file is forced to 0644 either way: staging goes through
// os.CreateTemp (0600) and ffmpeg, so without this the download could inherit an
// owner-only mode instead of the world-readable one the direct-write path used.
func finalizeToOutput(src, dst string) error {
	if err := os.Rename(src, dst); err != nil {
		if !errors.Is(err, syscall.EXDEV) {
			return err
		}
		if err := copyIntoPlace(src, dst); err != nil {
			return err
		}
	}
	return os.Chmod(dst, 0o644)
}

// copyIntoPlace materialises src at dst across a filesystem boundary: copy to a
// dst-side .part, flush, then atomically rename into place. dst never appears as
// a partial file. src is left for the caller to remove.
func copyIntoPlace(src, dst string) error {
	partPath := dst + partSuffix
	if err := copyFile(src, partPath); err != nil {
		_ = os.Remove(partPath)
		return err
	}
	if err := os.Rename(partPath, dst); err != nil {
		_ = os.Remove(partPath)
		return err
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func cleanupFailedOutput(outPath string) {
	_ = os.Remove(outPath)
	_ = os.Remove(outPath + partSuffix)
	_ = os.Remove(strings.TrimSuffix(outPath, ".m4a") + ".lrc")
	_ = os.Remove(strings.TrimSuffix(outPath, ".m4a") + ".ttml")
}

func (d *Downloader) failItem(ctx context.Context, reporter jobs.Reporter, job domain.Job, item domain.JobItem, err error) error {
	item.Status = domain.ItemFailed
	item.Error = err.Error()
	if item.StatusMessage == "" || item.Attempt == 0 {
		item.StatusMessage = "Download failed: " + err.Error()
	}
	_ = reporter.UpdateItem(ctx, item)
	_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "item_failed", Message: err.Error()})
	return err
}
