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

	"amdl/backend/internal/applemusic"
	"amdl/backend/internal/config"
	"amdl/backend/internal/domain"
	"amdl/backend/internal/jobs"
	"amdl/backend/internal/storage"
	"amdl/backend/internal/wrapper"
)

type Downloader struct {
	cfg     config.Config
	catalog *applemusic.CatalogClient
	wrapper *wrapper.Client
	tools   *ToolChecker
	http    *http.Client
	mp4     *MP4Processor
	logger  *slog.Logger
}

func NewDownloader(cfg config.Config, catalog *applemusic.CatalogClient, wrapperClient *wrapper.Client, tools *ToolChecker, logger *slog.Logger) *Downloader {
	return &Downloader{cfg: cfg, catalog: catalog, wrapper: wrapperClient, tools: tools, http: newHTTPClient(), mp4: newMP4Processor(cfg), logger: logger}
}

func (d *Downloader) ValidateRequest(ctx context.Context, req domain.DownloadRequest) (jobs.ValidationResult, error) {
	parsed, err := applemusic.ParseWithAlbumTrackMode(req.URL, d.cfg.Catalog.AlbumTrackURLMode)
	if err != nil {
		if strings.Contains(err.Error(), "album_track_url_mode") {
			return jobs.ValidationResult{}, &jobs.RequestError{Code: "invalid_configuration", Message: err.Error(), Cause: err}
		}
		return jobs.ValidationResult{}, &jobs.RequestError{Code: "invalid_url", Message: err.Error(), Cause: err}
	}
	if parsed.Type == applemusic.TypeArtist || parsed.Type == applemusic.TypeVideo {
		message := fmt.Sprintf("%s download is not implemented", parsed.Type)
		return jobs.ValidationResult{}, &jobs.RequestError{Code: "unsupported_input", Message: message}
	}
	if err := d.validateStorefront(ctx, parsed.Storefront); err != nil {
		return jobs.ValidationResult{}, err
	}
	return jobs.ValidationResult{Type: string(parsed.Type), Storefront: parsed.Storefront}, nil
}

func (d *Downloader) validateStorefront(ctx context.Context, storefront string) error {
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
	if parsed.Type == applemusic.TypeArtist || parsed.Type == applemusic.TypeVideo {
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

	resolved, _, err := retryValue(ctx, d.cfg.Download.Retries, retryBackoff, func(int) (resolvedCollection, error) {
		return d.resolveCollection(ctx, parsed)
	}, func(failure retryFailure) {
		d.emitRetryEvent(ctx, reporter, job.ID, "", "resolve_tracks", "", failure)
	})
	if err != nil {
		return err
	}
	tracks := resolved.Tracks
	collectionName := resolved.Name
	if parsed.Type == applemusic.TypePlaylist && d.cfg.Download.SavePlaylistCover && len(tracks) > 0 {
		firstOutput := outputPath(d.cfg, tracks[0], parsed.Type, 1, "", collectionName)
		if coverErr := d.savePlaylistCover(ctx, resolved.ArtworkURL, filepath.Dir(firstOutput)); coverErr != nil {
			_ = reporter.Event(ctx, domain.Event{JobID: job.ID, Type: "standalone_cover_failed", Phase: "playlist_cover", Message: coverErr.Error()})
		}
	}
	job.TotalItems = len(tracks)
	if err := reporter.SetJob(ctx, job); err != nil {
		return err
	}
	if len(tracks) == 0 {
		return fmt.Errorf("no downloadable songs found")
	}
	folderArtist := collectionFolderArtist(parsed.Type, tracks)

	items := make([]domain.JobItem, len(tracks))
	for i, track := range tracks {
		items[i] = domain.JobItem{
			ID: storage.NewID("item"), JobID: job.ID, AdamID: track.ID, Kind: "song", Index: i + 1,
			Title: track.Name, Artist: track.ArtistName, Album: track.AlbumName, Status: domain.ItemQueued,
		}
		if err := reporter.AddItem(ctx, items[i]); err != nil {
			return err
		}
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
			if err := d.processTrack(ctx, job, items[i], tracks[i], parsed.Storefront, parsed.Type, collectionName, i+1, folderArtist, reporter); err != nil {
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

func collectionFolderArtist(collectionType applemusic.URLType, tracks []applemusic.Song) string {
	if collectionType != applemusic.TypeAlbum || len(tracks) == 0 {
		return ""
	}
	if tracks[0].AlbumArtist != "" {
		return tracks[0].AlbumArtist
	}
	return tracks[0].ArtistName
}

type resolvedCollection struct {
	Tracks     []applemusic.Song
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
		return resolvedCollection{Tracks: []applemusic.Song{song}}, nil
	case applemusic.TypeAlbum:
		album, err := d.catalog.Album(ctx, parsed.Storefront, parsed.ID)
		if err != nil {
			return resolvedCollection{}, err
		}
		return resolvedCollection{Tracks: album.Tracks}, nil
	case applemusic.TypePlaylist:
		playlist, err := d.catalog.Playlist(ctx, parsed.Storefront, parsed.ID)
		if err != nil {
			return resolvedCollection{}, err
		}
		return resolvedCollection{Tracks: playlist.Tracks, Name: playlist.Name, ArtworkURL: playlist.ArtworkURL}, nil
	default:
		return resolvedCollection{}, fmt.Errorf("unsupported input type %s", parsed.Type)
	}
}

func (d *Downloader) processTrack(ctx context.Context, job domain.Job, item domain.JobItem, initial applemusic.Song, storefront string, collectionType applemusic.URLType, collectionName string, playlistIndex int, folderArtist string, reporter jobs.Reporter) error {
	// set updates item state and emits an item_progress SSE event.
	// The full JobItem is embedded in the event Payload so the frontend can
	// update the UI directly from SSE without any additional HTTP round-trips.
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
			_ = reporter.Event(ctx, domain.Event{
				JobID:   job.ID,
				ItemID:  item.ID,
				Type:    "item_progress",
				Phase:   string(status),
				Message: message,
				Payload: marshalPayload(item), // full item state for frontend
			})
		}
	}

	set(domain.ItemResolving, 0.01, "resolving metadata")

	song, metadataAttempts, err := retryValue(ctx, d.cfg.Download.Retries, retryBackoff, func(attempt int) (applemusic.Song, error) {
		d.setItemAttempt(ctx, reporter, &item, "metadata", attempt, maxAttempts(d.cfg.Download.Retries), fmt.Sprintf("正在获取歌曲元数据（%d/%d）", attempt, maxAttempts(d.cfg.Download.Retries)))
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
	_ = reporter.UpdateItem(ctx, item)

	outPath := outputPath(d.cfg, song, collectionType, playlistIndex, folderArtist, collectionName)
	if d.cfg.Download.SaveAlbumCover || d.cfg.Download.SaveArtistCover {
		if coverErr := d.saveStandaloneCovers(ctx, song, collectionType, storefront, outPath); coverErr != nil {
			item.StatusMessage = "独立封面保存失败，继续下载：" + coverErr.Error()
			_ = reporter.UpdateItem(ctx, item)
			_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "standalone_cover_failed", Message: coverErr.Error()})
		}
	}
	if _, err := os.Stat(outPath); err == nil && !job.Force {
		item.Status = domain.ItemSkipped
		item.Progress = 1
		item.RetryKind = ""
		item.Attempt = 0
		item.MaxAttempts = 0
		item.StatusMessage = "文件已存在，已跳过"
		item.OutputPath = outPath
		_ = reporter.UpdateItem(ctx, item)
		_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "item_skipped", Message: "already exists"})
		return nil
	}
	if job.Force {
		cleanupFailedOutput(outPath)
		_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "item_overwrite", Message: "force overwrite enabled"})
	}

	var cover []byte
	if d.cfg.Download.EmbedCover {
		coverURLs := trackCoverURLs(song, collectionType)
		var coverAttempts int
		cover, coverAttempts, err = retryValue(ctx, d.cfg.Download.Retries, retryBackoff, func(attempt int) ([]byte, error) {
			d.setItemAttempt(ctx, reporter, &item, "cover", attempt, maxAttempts(d.cfg.Download.Retries), fmt.Sprintf("正在获取封面（%d/%d）", attempt, maxAttempts(d.cfg.Download.Retries)))
			return d.catalog.FetchCover(ctx, coverURLs, d.cfg.Download.CoverFormat, d.cfg.Download.CoverSize)
		}, func(failure retryFailure) {
			d.setRetryFailure(ctx, reporter, &item, "cover", "cover", failure)
			d.emitRetryEvent(ctx, reporter, job.ID, item.ID, "cover", "", failure)
		})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			item.StatusMessage = "封面获取重试耗尽，继续下载但不嵌入封面：" + err.Error()
			_ = reporter.UpdateItem(ctx, item)
		} else if coverAttempts > 1 {
			d.emitRecoveredEvent(ctx, reporter, job.ID, item.ID, "cover", "", coverAttempts)
		}
	}
	lyrics := ""
	if (d.cfg.Download.EmbedLyrics || d.cfg.Download.SaveLyricsFile) && song.HasLyrics {
		raw, lyricsAttempts, lyricsErr := retryValue(ctx, d.cfg.Download.Retries, retryBackoff, func(attempt int) (string, error) {
			d.setItemAttempt(ctx, reporter, &item, "lyrics", attempt, maxAttempts(d.cfg.Download.Retries), fmt.Sprintf("正在获取歌词（%d/%d）", attempt, maxAttempts(d.cfg.Download.Retries)))
			return d.wrapper.Lyrics(ctx, song.ID, storefront, d.cfg.Catalog.Language)
		}, func(failure retryFailure) {
			d.setRetryFailure(ctx, reporter, &item, "lyrics", "lyrics", failure)
			d.emitRetryEvent(ctx, reporter, job.ID, item.ID, "lyrics", "", failure)
		})
		if lyricsErr == nil {
			lyrics = convertLyrics(raw, d.cfg.Download.LyricsFormat)
			if lyricsAttempts > 1 {
				d.emitRecoveredEvent(ctx, reporter, job.ID, item.ID, "lyrics", "", lyricsAttempts)
			}
		} else {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			item.StatusMessage = "歌词获取重试耗尽，继续下载但不嵌入歌词：" + lyricsErr.Error()
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
		codecRetries := retriesForCodec(d.cfg.Download.Retries, codecIndex)
		if codecIndex > 0 {
			item.StatusMessage = fmt.Sprintf("编码 %s 失败，回退到 %s", strings.ToUpper(codecs[codecIndex-1]), strings.ToUpper(codec))
			_ = reporter.UpdateItem(ctx, item)
			_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "codec_fallback", Phase: codec, Message: item.StatusMessage, Payload: marshalPayload(map[string]any{
				"from_codec": codecs[codecIndex-1], "to_codec": codec, "reason": codecFailureReason(lastErr),
			})})
		}
		item.Codec = codec
		_, attempts, downloadErr := retryValue(ctx, codecRetries, retryBackoff, func(attempt int) (struct{}, error) {
			d.setItemAttempt(ctx, reporter, &item, "download", attempt, maxAttempts(codecRetries), fmt.Sprintf("正在下载 %s（%d/%d）", strings.ToUpper(codec), attempt, maxAttempts(codecRetries)))
			if codec == "aac-lc" {
				return struct{}{}, d.downloadAACLC(ctx, job, &item, song, lyrics, cover, outPath, reporter, set)
			}
			return struct{}{}, d.downloadEnhancedCodec(ctx, job, &item, song, codec, lyrics, cover, outPath, reporter, set)
		}, func(failure retryFailure) {
			cleanupFailedOutput(outPath)
			d.setRetryFailure(ctx, reporter, &item, "download", strings.ToUpper(codec), failure)
			d.emitRetryEvent(ctx, reporter, job.ID, item.ID, "download", codec, failure)
		})
		if downloadErr != nil {
			lastErr = downloadErr
			_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "codec_failed", Phase: codec, Message: downloadErr.Error(), Payload: marshalPayload(map[string]any{
				"codec": codec, "attempts": attempts, "max_attempts": maxAttempts(codecRetries), "error": downloadErr.Error(),
			})})
			continue
		}

		item.Attempt = attempts
		if codecIndex > 0 {
			item.StatusMessage = fmt.Sprintf("已回退为 %s 并下载完成", strings.ToUpper(codec))
		} else if attempts > 1 {
			item.StatusMessage = fmt.Sprintf("%s 在第 %d 次尝试成功", strings.ToUpper(codec), attempts)
		} else {
			item.StatusMessage = fmt.Sprintf("%s 下载完成", strings.ToUpper(codec))
		}
		_ = reporter.UpdateItem(ctx, item)
		_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "item_completed", Message: outPath, Payload: marshalPayload(map[string]any{
			"codec": codec, "attempt": attempts, "max_attempts": maxAttempts(codecRetries), "fallback_from": fallbackCodec(codecs, codecIndex),
		})})
		if attempts > 1 {
			d.emitRecoveredEvent(ctx, reporter, job.ID, item.ID, "download", codec, attempts)
		}
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no configured codec succeeded")
	}
	return d.failItem(ctx, reporter, job, item, lastErr)
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

func retriesForCodec(configuredRetries, codecIndex int) int {
	if codecIndex > 0 {
		return 0
	}
	return configuredRetries
}

func (d *Downloader) downloadEnhancedCodec(ctx context.Context, job domain.Job, item *domain.JobItem, song applemusic.Song, codec, lyrics string, cover []byte, outPath string, reporter jobs.Reporter, set func(domain.ItemStatus, float64, string)) error {
	set(domain.ItemDownloading, 0.03, "selecting manifest")
	master := song.EnhancedHLS
	if codec == "alac" {
		m3u8, err := d.wrapper.M3U8(ctx, song.ID)
		if err != nil {
			return fmt.Errorf("request device m3u8: %w", err)
		}
		master = m3u8
	}
	if master == "" {
		return fmt.Errorf("no enhanced hls manifest")
	}
	info, err := extractMedia(ctx, d.http, master, codec, d.cfg.Download.ALACMaxSampleRate, d.cfg.Download.ALACMaxBitDepth)
	if err != nil {
		return fmt.Errorf("select %s media: %w", codec, err)
	}
	payload, _ := json.Marshal(map[string]any{"codec_id": info.CodecID, "bit_depth": info.BitDepth, "sample_rate": info.SampleRate, "attempt": item.Attempt, "max_attempts": item.MaxAttempts})
	_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "codec_selected", Phase: codec, Payload: string(payload)})

	set(domain.ItemDownloading, 0.05, "downloading encrypted media")
	// Stream-download with per-chunk progress from 5% → 55%
	raw, err := downloadBytes(ctx, d.http, info.MediaURI, func(p float64) {
		if p < 0 {
			return // Content-Length unknown, stay at 5%
		}
		// map [0,1] → [0.05, 0.55]
		set(domain.ItemDownloading, 0.05+p*0.50, fmt.Sprintf("downloading %.0f%%", p*100))
	})
	if err != nil {
		return fmt.Errorf("download encrypted media: %w", err)
	}
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
	var mediaBytes []byte
	for _, sample := range decryptedSamples {
		mediaBytes = append(mediaBytes, sample...)
	}
	set(domain.ItemRemuxing, 0.90, "remuxing")
	outBytes, err := d.mp4.encapsulate(ctx, extracted, mediaBytes)
	if err != nil {
		return fmt.Errorf("encapsulate decrypted media: %w", err)
	}
	if codec != "ec3" && codec != "ac3" {
		fixed, err := d.mp4.fixEncapsulate(ctx, outBytes)
		if err != nil {
			return fmt.Errorf("fix encapsulation: %w", err)
		}
		outBytes = fixed
	}
	if codec == "aac" || codec == "aac-downmix" || codec == "aac-binaural" {
		fixed, err := d.mp4.fixESDS(ctx, raw, outBytes)
		if err != nil {
			return fmt.Errorf("fix esds: %w", err)
		}
		outBytes = fixed
	}
	if d.cfg.Download.CheckIntegrity && !d.mp4.checkIntegrity(ctx, outBytes) {
		return fmt.Errorf("integrity check failed")
	}
	set(domain.ItemSaving, 0.94, "saving")
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	if err := os.WriteFile(outPath, outBytes, 0o644); err != nil {
		return fmt.Errorf("write output file: %w", err)
	}
	if d.cfg.Download.SaveLyricsFile && lyrics != "" {
		ext := ".lrc"
		if d.cfg.Download.LyricsFormat == "ttml" {
			ext = ".ttml"
		}
		if err := os.WriteFile(stringsTrimSuffix(outPath, ".m4a")+ext, []byte(lyrics), 0o644); err != nil {
			return fmt.Errorf("write lyrics file: %w", err)
		}
	}
	set(domain.ItemTagging, 0.97, "writing metadata")
	if err := d.mp4.writeMetadata(ctx, outPath, song, lyrics, cover, extracted); err != nil {
		return fmt.Errorf("write metadata: %w", err)
	}
	item.Status = domain.ItemCompleted
	item.Progress = 1
	item.OutputPath = outPath
	item.Codec = codec
	return nil
}

func (d *Downloader) downloadAACLC(ctx context.Context, job domain.Job, item *domain.JobItem, song applemusic.Song, lyrics string, cover []byte, outPath string, reporter jobs.Reporter, set func(domain.ItemStatus, float64, string)) error {
	set(domain.ItemDownloading, 0.03, "requesting AAC-LC WebPlayback asset")
	playlistURL, err := d.wrapper.WebPlayback(ctx, song.ID)
	if err != nil {
		return fmt.Errorf("request AAC-LC WebPlayback: %w", err)
	}
	media, err := extractAACLCMedia(ctx, d.http, playlistURL)
	if err != nil {
		return fmt.Errorf("parse AAC-LC media playlist: %w", err)
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
		return fmt.Errorf("download encrypted AAC-LC media: %w", err)
	}
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
	if d.cfg.Download.CheckIntegrity && !d.mp4.checkIntegrity(ctx, decrypted) {
		return fmt.Errorf("AAC-LC integrity check failed")
	}

	set(domain.ItemSaving, 0.94, "saving AAC-LC")
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	if err := os.WriteFile(outPath, decrypted, 0o644); err != nil {
		return fmt.Errorf("write AAC-LC output file: %w", err)
	}
	if d.cfg.Download.SaveLyricsFile && lyrics != "" {
		ext := ".lrc"
		if d.cfg.Download.LyricsFormat == "ttml" {
			ext = ".ttml"
		}
		if err := os.WriteFile(stringsTrimSuffix(outPath, ".m4a")+ext, []byte(lyrics), 0o644); err != nil {
			return fmt.Errorf("write lyrics file: %w", err)
		}
	}
	set(domain.ItemTagging, 0.97, "writing AAC-LC metadata")
	if err := d.mp4.writeMetadata(ctx, outPath, song, lyrics, cover, songInfo{Codec: "aac-lc"}); err != nil {
		return fmt.Errorf("write AAC-LC metadata: %w", err)
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

func (d *Downloader) setRetryFailure(ctx context.Context, reporter jobs.Reporter, item *domain.JobItem, kind, operation string, failure retryFailure) {
	item.RetryKind = kind
	item.Attempt = failure.Attempt
	item.MaxAttempts = failure.MaxAttempts
	if failure.WillRetry {
		item.StatusMessage = fmt.Sprintf("%s 第 %d/%d 次尝试失败，%s 后重试：%v", operation, failure.Attempt, failure.MaxAttempts, failure.Delay, failure.Err)
	} else {
		item.StatusMessage = fmt.Sprintf("%s 已尝试 %d 次仍失败：%v", operation, failure.Attempt, failure.Err)
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

func cleanupFailedOutput(outPath string) {
	_ = os.Remove(outPath)
	_ = os.Remove(stringsTrimSuffix(outPath, ".m4a") + ".lrc")
	_ = os.Remove(stringsTrimSuffix(outPath, ".m4a") + ".ttml")
}

func (d *Downloader) failItem(ctx context.Context, reporter jobs.Reporter, job domain.Job, item domain.JobItem, err error) error {
	item.Status = domain.ItemFailed
	item.Error = err.Error()
	if item.StatusMessage == "" || item.Attempt == 0 {
		item.StatusMessage = "下载失败：" + err.Error()
	}
	_ = reporter.UpdateItem(ctx, item)
	_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "item_failed", Message: err.Error()})
	return err
}

func stringsTrimSuffix(v, suffix string) string {
	if len(v) >= len(suffix) && v[len(v)-len(suffix):] == suffix {
		return v[:len(v)-len(suffix)]
	}
	return v
}
