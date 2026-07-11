package media

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"amdl/internal/applemusic"
	"amdl/internal/config"
	"amdl/internal/domain"
	"amdl/internal/jobs"
	"amdl/internal/storage"
	"amdl/internal/wrapper"
)

type Downloader struct {
	cfg     config.Config
	catalog downloaderCatalog
	wrapper downloaderWrapper
	tools   *ToolChecker
	http    *http.Client
	mp4     *MP4Processor
	logger  *slog.Logger
}

type downloaderCatalog interface {
	Song(context.Context, string, string) (applemusic.Song, error)
	Album(context.Context, string, string) (applemusic.Collection, error)
	Playlist(context.Context, string, string) (applemusic.Collection, error)
	ArtistAlbums(context.Context, string, string) (applemusic.ArtistAlbums, error)
	Artist(context.Context, string, string) (applemusic.Artist, error)
	FetchCover(context.Context, []string, string, string) ([]byte, error)
}

type downloaderWrapper interface {
	Status(context.Context) (wrapper.Status, error)
	M3U8(context.Context, string) (string, error)
	Lyrics(context.Context, string, wrapper.LyricsRequestOptions) (string, error)
	WebPlayback(context.Context, string) (string, error)
	Decrypt(context.Context, string, []wrapper.DecryptSample, func(int, int)) ([][]byte, error)
	License(context.Context, string, string, string) (string, error)
}

type selectedDownloadMedia struct {
	info selectedMediaInfo
	raw  []byte
}

func NewDownloader(cfg config.Config, catalog *applemusic.CatalogClient, wrapperClient *wrapper.Client, tools *ToolChecker, logger *slog.Logger) *Downloader {
	return &Downloader{cfg: cfg, catalog: catalog, wrapper: wrapperClient, tools: tools, http: newHTTPClient(), mp4: newMP4Processor(cfg), logger: logger}
}

func (d *Downloader) ValidateRequest(ctx context.Context, url string) (jobs.ValidationResult, error) {
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
	parsed, err := applemusic.ParseWithAlbumTrackMode(job.Input, d.cfg.Catalog.AlbumTrackURLMode)
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
	if err := reporter.Event(ctx, domain.Event{JobID: job.ID, Type: "resolved_input", Message: string(parsed.Type)}); err != nil {
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
	if parsed.Type == applemusic.TypePlaylist && d.cfg.Download.SavePlaylistCover && len(tracks) > 0 && !d.cfg.Simulate.Enabled {
		folder := playlistFolderPath(d.cfg, tracks[0], collectionName, collectionID)
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
	if len(tracks) == 0 {
		return fmt.Errorf("no downloadable songs found")
	}
	folderArtist := collectionFolderArtist(parsed.Type, tracks)

	items, finished, err := syncJobItems(ctx, job, tracks, reporter)
	if err != nil {
		return err
	}

	parallel := d.cfg.Download.MaxParallelTracks
	if parallel <= 0 {
		parallel = 1
	}
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for i := range tracks {
		if finished[i] {
			// Finished in a previous run of this job; keep the item as-is.
			continue
		}
		i := i
		select {
		case <-ctx.Done():
			return ctx.Err()
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := d.processTrack(ctx, job, items[i], tracks[i], parsed.Storefront, parsed.Type, collectionName, collectionID, i+1, folderArtist, reporter); err != nil {
				d.logger.Error("track failed", "adam_id", tracks[i].ID, "error", err)
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
			prev.Index = i + 1
			if prev.Finished() {
				finished[i] = true
			} else {
				prev.ResetForRetry()
			}
			items[i] = prev
			if !finished[i] || indexChanged {
				if err := reporter.UpdateItem(ctx, prev); err != nil {
					return nil, nil, err
				}
			}
			continue
		}
		items[i] = domain.JobItem{
			ID: storage.NewID("item"), JobID: job.ID, AdamID: track.ID, Kind: "song", Index: i + 1,
			Title: track.Name, Artist: track.ArtistName, Album: track.AlbumName,
			ArtworkURL: firstNonEmpty(track.ArtworkURL, track.AlbumArtworkURL), Status: domain.ItemQueued,
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
		playlist, err := d.catalog.Playlist(ctx, parsed.Storefront, parsed.ID)
		if err != nil {
			return resolvedCollection{}, err
		}
		return resolvedCollection{Tracks: playlist.Tracks, ID: playlist.ID, Name: playlist.Name, ArtworkURL: playlist.ArtworkURL}, nil
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
	seen := make(map[string]struct{})
	for _, summary := range albums {
		album, err := d.catalog.Album(ctx, storefront, summary.ID)
		if err != nil {
			return nil, err
		}
		for _, track := range album.Tracks {
			if _, exists := seen[track.ID]; exists {
				continue
			}
			seen[track.ID] = struct{}{}
			tracks = append(tracks, track)
		}
	}
	return tracks, nil
}

func (d *Downloader) processTrack(ctx context.Context, job domain.Job, item domain.JobItem, initial applemusic.Song, storefront string, collectionType applemusic.URLType, collectionName, collectionID string, playlistIndex int, folderArtist string, reporter jobs.Reporter) error {
	// set updates item state and emits an item_progress SSE event.
	// The full JobItem is embedded in the event Payload so the frontend can
	// update the UI directly from SSE without any additional HTTP round-trips,
	// except artwork_url: cover art doesn't change during a download, so it's
	// stripped from the event to keep progress/status pushes lightweight — the
	// frontend already has it from the initial REST response.
	// To avoid flooding the stream, we only emit when status changes or
	// progress moves by at least 1 percentage point.
	var lastEmittedStatus domain.ItemStatus
	var lastEmittedProgress float64 = -1
	set := func(status domain.ItemStatus, progress float64, message string) {
		item.Status = status
		item.Progress = progress
		item.StatusMessage = message
		_ = reporter.UpdateItem(ctx, item)
		progPct := math.Round(progress * 100)
		lastPct := math.Round(lastEmittedProgress * 100)
		if status != lastEmittedStatus || progPct != lastPct {
			lastEmittedStatus = status
			lastEmittedProgress = progress
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
		return d.catalog.Song(ctx, storefront, initial.ID)
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
	_ = reporter.UpdateItem(ctx, item)

	if d.cfg.Simulate.Enabled {
		// Test mode: real catalog metadata was resolved above, but everything
		// from here on (covers, lyrics, media selection, transfer, decrypt,
		// disk writes) is simulated with an identical status/event lifecycle.
		return d.simulateTrack(ctx, job, &item, song, collectionType, collectionName, collectionID, playlistIndex, folderArtist, reporter, set)
	}

	albumCoverDir, artistCoverDir := standaloneCoverDirs(d.cfg, song, collectionType, folderArtist)
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
			return d.catalog.FetchCover(ctx, coverURLs, d.cfg.Download.CoverFormat, d.cfg.Download.CoverSize)
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
			if convertErr != nil {
				item.StatusMessage = "Lyrics conversion failed; continuing without embedded lyrics: " + convertErr.Error()
				_ = reporter.UpdateItem(ctx, item)
			} else {
				lyrics = converted
				if lyricsAttempts > 1 {
					d.emitRecoveredEvent(ctx, reporter, job.ID, item.ID, "lyrics", "", lyricsAttempts)
				}
			}
		} else {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			item.StatusMessage = "Lyrics fetch retries exhausted; continuing without embedded lyrics: " + lyricsErr.Error()
			_ = reporter.UpdateItem(ctx, item)
		}
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

		// Fetch phase: acquire the still-encrypted media into memory. Retried on
		// its own so a later decrypt failure doesn't force a redundant re-download
		// of bytes that were already fetched successfully.
		var aacMedia aacLCMedia
		var enhanced selectedDownloadMedia
		var rawAACLC []byte
		_, fetchAttempts, fetchErr := retryValue(ctx, codecMaxAttempts, retryBackoff, func(attempt int) (struct{}, error) {
			if codec == "aac-lc" {
				d.setItemAttempt(ctx, reporter, &item, "download", attempt, clampAttempts(codecMaxAttempts), fmt.Sprintf("Downloading %s (%d/%d)", strings.ToUpper(codec), attempt, clampAttempts(codecMaxAttempts)))
				attemptOutPath = outputPath(d.cfg, song, collectionType, playlistIndex, folderArtist, collectionName, collectionID, codec, "256Kbps")
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
			if attemptOutPath != "" {
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
			return nil
		}
		if fetchErr != nil {
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
			if attemptOutPath != "" {
				cleanupFailedOutput(attemptOutPath)
			}
			d.setRetryFailure(ctx, reporter, &item, "decrypt", strings.ToUpper(codec), failure)
			d.emitRetryEvent(ctx, reporter, job.ID, item.ID, "decrypt", codec, failure)
		})
		if decryptErr != nil {
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
		// playlist comes from the authorized device manifest instead.
		m3u8, err := d.wrapper.M3U8(ctx, song.ID)
		if err != nil {
			return selectedDownloadMedia{}, fmt.Errorf("request device m3u8: %w", err)
		}
		master = m3u8
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
	// Stream-download with per-chunk progress from 5% → 55%
	raw, err := downloadBytes(ctx, d.http, selected.info.MediaURI, func(p float64) {
		if p < 0 {
			return // Content-Length unknown, stay at 5%
		}
		// map [0,1] → [0.05, 0.55]
		set(domain.ItemDownloading, 0.05+p*0.50, fmt.Sprintf("%s download %.0f%%", codecName, p*100))
	})
	if err != nil {
		return selectedDownloadMedia{}, fmt.Errorf("download encrypted media: %w", err)
	}
	selected.raw = raw
	return selected, nil
}

func (d *Downloader) downloadEnhancedCodec(ctx context.Context, job domain.Job, item *domain.JobItem, song applemusic.Song, codec, lyrics string, cover []byte, outPath string, selected selectedDownloadMedia, reporter jobs.Reporter, set func(domain.ItemStatus, float64, string)) error {
	info := selected.info
	raw := selected.raw
	set(domain.ItemDecrypting, 0.55, "extracting samples")
	extracted, err := d.mp4.extractSong(ctx, raw, codec)
	if err != nil {
		return fmt.Errorf("extract encrypted samples: %w", err)
	}
	samples := make([]wrapper.DecryptSample, 0, len(extracted.Samples))
	for i, sample := range extracted.Samples {
		keyIndex := sample.DescIndex
		if keyIndex < 0 || keyIndex >= len(info.Keys) {
			keyIndex = 0
		}
		samples = append(samples, wrapper.DecryptSample{Key: info.Keys[keyIndex], Index: i, Data: sample.Data})
	}
	// Decrypt with per-sample progress from 55% → 90%
	decryptedSamples, err := d.wrapper.Decrypt(ctx, song.ID, samples, func(received, total int) {
		if total <= 0 {
			return
		}
		p := float64(received) / float64(total)
		// map [0,1] → [0.55, 0.90]
		set(domain.ItemDecrypting, 0.55+p*0.35, fmt.Sprintf("decrypting %d/%d samples", received, total))
	})
	if err != nil {
		return fmt.Errorf("decrypt samples: %w", err)
	}
	set(domain.ItemRemuxing, 0.90, "remuxing")
	outBytes, err := d.mp4.encapsulate(ctx, extracted, decryptedSamples)
	if err != nil {
		return fmt.Errorf("encapsulate decrypted media: %w", err)
	}
	// Flatten the fragmented MP4 produced by mp4ff into a regular progressive
	// MP4 (also normalises the ftyp brand). The decoder configuration is carried
	// over from the original init segment, so no esds fixup is required.
	fixed, err := d.mp4.fixEncapsulate(ctx, outBytes)
	if err != nil {
		return fmt.Errorf("fix encapsulation: %w", err)
	}
	outBytes = fixed
	if d.cfg.Download.CheckIntegrity && !d.mp4.checkIntegrity(ctx, outBytes) {
		if codec != "alac" {
			return fmt.Errorf("integrity check failed")
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
		if !d.mp4.checkIntegrity(ctx, repaired) {
			return fmt.Errorf("integrity check failed after alac repair")
		}
		outBytes = repaired
	}
	set(domain.ItemSaving, 0.94, "saving")
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	// Write to a .part name and only rename onto outPath once metadata is in
	// place: handleExistingOutput trusts bare existence at the final path when
	// deciding to skip, so a crash between the audio write and tagging must
	// never leave a truncated or untagged file there.
	partPath := outPath + partSuffix
	if err := os.WriteFile(partPath, outBytes, 0o644); err != nil {
		return fmt.Errorf("write output file: %w", err)
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
	if err := d.mp4.writeMetadata(ctx, partPath, song, lyrics, cover, extracted); err != nil {
		return fmt.Errorf("write metadata: %w", err)
	}
	if err := os.Rename(partPath, outPath); err != nil {
		return fmt.Errorf("finalize output file: %w", err)
	}
	item.Status = domain.ItemCompleted
	item.Progress = 1
	item.OutputPath = outPath
	item.Codec = codec
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
	// Normalise the container (flatten + M4A brand + create the
	// moov.udta.meta.ilst structure that go-mp4tag writes into). The decrypted
	// WebPlayback asset has no udta box, so tagging would otherwise fail.
	decrypted, err = d.mp4.fixEncapsulate(ctx, decrypted)
	if err != nil {
		return fmt.Errorf("normalize AAC-LC container: %w", err)
	}
	if d.cfg.Download.CheckIntegrity && !d.mp4.checkIntegrity(ctx, decrypted) {
		return fmt.Errorf("AAC-LC integrity check failed")
	}

	set(domain.ItemSaving, 0.94, "saving AAC-LC")
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	// Same .part-then-rename finalize as downloadEnhancedCodec: the final path
	// must only ever hold a complete, tagged file.
	partPath := outPath + partSuffix
	if err := os.WriteFile(partPath, decrypted, 0o644); err != nil {
		return fmt.Errorf("write AAC-LC output file: %w", err)
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
	if err := d.mp4.writeMetadata(ctx, partPath, song, lyrics, cover, songInfo{Codec: "aac-lc"}); err != nil {
		return fmt.Errorf("write AAC-LC metadata: %w", err)
	}
	if err := os.Rename(partPath, outPath); err != nil {
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
