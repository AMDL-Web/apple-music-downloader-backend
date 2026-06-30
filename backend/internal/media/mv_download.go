package media

import (
	"context"
	"encoding/base64"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"amdl/backend/internal/applemusic"
	"amdl/backend/internal/domain"
	"amdl/backend/internal/jobs"
	"amdl/backend/internal/storage"
)

// processMusicVideo downloads, decrypts, muxes and tags a single Apple Music
// music video. It mirrors processTrack but operates on a single item because a
// music-video URL always resolves to exactly one asset.
func (d *Downloader) processMusicVideo(ctx context.Context, job domain.Job, parsed applemusic.ParsedURL, reporter jobs.Reporter) error {
	item := domain.JobItem{
		ID: storage.NewID("item"), JobID: job.ID, AdamID: parsed.ID, Kind: "music-video", Index: 1,
		Status: domain.ItemQueued,
	}
	if err := reporter.AddItem(ctx, item); err != nil {
		return err
	}

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
				Payload: marshalPayload(item),
			})
		}
	}

	set(domain.ItemResolving, 0.01, "正在获取 MV 元数据")
	mv, metaAttempts, err := retryValue(ctx, d.cfg.Download.Retries, retryBackoff, func(attempt int) (applemusic.MusicVideo, error) {
		d.setItemAttempt(ctx, reporter, &item, "metadata", attempt, maxAttempts(d.cfg.Download.Retries), fmt.Sprintf("正在获取 MV 元数据（%d/%d）", attempt, maxAttempts(d.cfg.Download.Retries)))
		return d.catalog.MusicVideo(ctx, parsed.Storefront, parsed.ID)
	}, func(failure retryFailure) {
		d.setRetryFailure(ctx, reporter, &item, "metadata", "metadata", failure)
		d.emitRetryEvent(ctx, reporter, job.ID, item.ID, "metadata", "", failure)
	})
	if err != nil {
		return d.failItem(ctx, reporter, job, item, err)
	}
	if metaAttempts > 1 {
		d.emitRecoveredEvent(ctx, reporter, job.ID, item.ID, "metadata", "", metaAttempts)
	}
	item.Title = mv.Name
	item.Artist = mv.ArtistName
	item.Album = mv.AlbumName
	item.Codec = "h264/aac"
	_ = reporter.UpdateItem(ctx, item)

	outPath := mvOutputPath(d.cfg, mv)
	if _, statErr := os.Stat(outPath); statErr == nil {
		item.Status = domain.ItemSkipped
		item.Progress = 1
		item.OutputPath = outPath
		item.RetryKind = ""
		item.Attempt = 0
		item.MaxAttempts = 0
		item.StatusMessage = "文件已存在，已跳过"
		_ = reporter.UpdateItem(ctx, item)
		_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "item_skipped", Message: "already exists"})
		return nil
	}

	if err := d.tools.Require(ctx); err != nil {
		return d.failItem(ctx, reporter, job, item, err)
	}

	var cover []byte
	if d.cfg.Download.EmbedCover && mv.ArtworkURL != "" {
		cover, _, err = retryValue(ctx, d.cfg.Download.Retries, retryBackoff, func(attempt int) ([]byte, error) {
			d.setItemAttempt(ctx, reporter, &item, "cover", attempt, maxAttempts(d.cfg.Download.Retries), fmt.Sprintf("正在获取封面（%d/%d）", attempt, maxAttempts(d.cfg.Download.Retries)))
			return d.catalog.Cover(ctx, mv.ArtworkURL, d.cfg.Download.CoverFormat, d.cfg.Download.CoverSize)
		}, func(failure retryFailure) {
			d.setRetryFailure(ctx, reporter, &item, "cover", "cover", failure)
		})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			cover = nil
			item.StatusMessage = "封面获取失败，继续下载但不嵌入封面：" + err.Error()
			_ = reporter.UpdateItem(ctx, item)
		}
	}

	_, attempts, downloadErr := retryValue(ctx, d.cfg.Download.Retries, retryBackoff, func(attempt int) (struct{}, error) {
		d.setItemAttempt(ctx, reporter, &item, "download", attempt, maxAttempts(d.cfg.Download.Retries), fmt.Sprintf("正在下载 MV（%d/%d）", attempt, maxAttempts(d.cfg.Download.Retries)))
		return struct{}{}, d.downloadMusicVideo(ctx, job, &item, mv, cover, outPath, reporter, set)
	}, func(failure retryFailure) {
		_ = os.Remove(outPath)
		d.setRetryFailure(ctx, reporter, &item, "download", "MV", failure)
		d.emitRetryEvent(ctx, reporter, job.ID, item.ID, "download", "mv", failure)
	})
	if downloadErr != nil {
		_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "codec_failed", Phase: "mv", Message: downloadErr.Error()})
		return d.failItem(ctx, reporter, job, item, downloadErr)
	}

	item.Attempt = attempts
	item.Status = domain.ItemCompleted
	item.Progress = 1
	item.OutputPath = outPath
	item.StatusMessage = "MV 下载完成"
	_ = reporter.UpdateItem(ctx, item)
	_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "item_completed", Message: outPath, Payload: marshalPayload(map[string]any{
		"codec": "mv", "attempt": attempts, "max_attempts": maxAttempts(d.cfg.Download.Retries),
	})})
	if attempts > 1 {
		d.emitRecoveredEvent(ctx, reporter, job.ID, item.ID, "download", "mv", attempts)
	}
	return nil
}

func (d *Downloader) downloadMusicVideo(ctx context.Context, job domain.Job, item *domain.JobItem, mv applemusic.MusicVideo, cover []byte, outPath string, reporter jobs.Reporter, set func(domain.ItemStatus, float64, string)) error {
	set(domain.ItemDownloading, 0.02, "请求 MV 播放清单")
	master, err := d.wrapper.WebPlayback(ctx, mv.ID)
	if err != nil {
		return fmt.Errorf("request MV webplayback: %w", err)
	}
	if master == "" {
		return fmt.Errorf("wrapper returned empty MV playlist (media-user-token 可能无效或过期)")
	}
	masterBody, err := downloadText(ctx, d.http, master)
	if err != nil {
		return fmt.Errorf("download MV master playlist: %w", err)
	}

	video, audioURL, err := selectMVStreams(masterBody, master, d.cfg.Download.MVMaxHeight, d.cfg.Download.MVAudioType)
	if err != nil {
		return err
	}
	_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "codec_selected", Phase: "mv", Payload: marshalPayload(map[string]any{
		"video_resolution": video.Resolution, "audio_group": video.AudioGroup, "allowed_cpc": video.AllowedCPC,
	})})

	tmpDir, err := os.MkdirTemp(d.cfg.Download.TempDir, "mv-*")
	if err != nil {
		return fmt.Errorf("create MV temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	vidPath := filepath.Join(tmpDir, "video.mp4")
	audPath := filepath.Join(tmpDir, "audio.mp4")

	// Video: 5% -> 50%
	if err := d.downloadMVStream(ctx, mv.ID, video.URI, vidPath, func(p float64) {
		set(domain.ItemDownloading, 0.05+p*0.45, fmt.Sprintf("下载视频流 %.0f%%", p*100))
	}, func(p float64) {
		set(domain.ItemDecrypting, 0.45+p*0.05, "解密视频流")
	}); err != nil {
		return fmt.Errorf("video stream: %w", err)
	}

	// Audio: 50% -> 85%
	if err := d.downloadMVStream(ctx, mv.ID, audioURL, audPath, func(p float64) {
		set(domain.ItemDownloading, 0.50+p*0.30, fmt.Sprintf("下载音频流 %.0f%%", p*100))
	}, func(p float64) {
		set(domain.ItemDecrypting, 0.80+p*0.05, "解密音频流")
	}); err != nil {
		return fmt.Errorf("audio stream: %w", err)
	}

	set(domain.ItemRemuxing, 0.90, "封装并写入 MV 元数据")
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	var coverPath string
	if d.cfg.Download.EmbedCover && len(cover) > 0 {
		coverPath = filepath.Join(tmpDir, "cover."+coverExt(d.cfg.Download.CoverFormat))
		if err := os.WriteFile(coverPath, cover, 0o644); err != nil {
			coverPath = ""
		}
	}

	set(domain.ItemTagging, 0.95, "封装并写入 MV 元数据")
	if err := d.muxMusicVideo(ctx, vidPath, audPath, outPath, mv, coverPath); err != nil {
		return err
	}
	return nil
}

func coverExt(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "png":
		return "png"
	default:
		return "jpg"
	}
}

// downloadMVStream downloads, parses, licenses, fetches and decrypts a single
// MV media playlist (video or audio) into outPath.
func (d *Downloader) downloadMVStream(ctx context.Context, adamID, playlistURL, outPath string, onDownload, onDecrypt func(float64)) error {
	mediaBody, err := downloadText(ctx, d.http, playlistURL)
	if err != nil {
		return fmt.Errorf("download media playlist: %w", err)
	}
	media, err := parseMVMedia(mediaBody, playlistURL)
	if err != nil {
		return err
	}
	challenge, parseLicense, err := newWidevineSessionFromPSSH(media.PSSHB64)
	if err != nil {
		return err
	}
	license, err := d.wrapper.License(ctx, adamID, base64.StdEncoding.EncodeToString(challenge), media.KeyURI)
	if err != nil {
		return fmt.Errorf("acquire Widevine license: %w", err)
	}
	raw, err := downloadMVSegments(ctx, d.http, media.InitURL, media.Segments, onDownload)
	if err != nil {
		return err
	}
	if onDecrypt != nil {
		onDecrypt(0.0)
	}
	decrypted, err := decryptWidevineMP4(raw, license, parseLicense)
	if err != nil {
		return fmt.Errorf("decrypt media: %w", err)
	}
	if onDecrypt != nil {
		onDecrypt(1.0)
	}
	if err := os.WriteFile(outPath, decrypted, 0o644); err != nil {
		return fmt.Errorf("write decrypted media: %w", err)
	}
	return nil
}

// muxMusicVideo remuxes the decrypted video and audio tracks into a single mp4
// and writes iTunes-style metadata (and optional cover) via MP4Box -itags.
func (d *Downloader) muxMusicVideo(ctx context.Context, videoPath, audioPath, outPath string, mv applemusic.MusicVideo, coverPath string) error {
	itags := buildMVITags(mv, coverPath)
	args := []string{"-quiet", "-itags", itags, "-add", videoPath, "-add", audioPath, "-keep-utc", "-new", outPath}
	if err := run(ctx, d.cfg.Tools.MP4Box, args...); err != nil {
		_ = os.Remove(outPath)
		return fmt.Errorf("mux MV: %w", err)
	}
	return nil
}

// buildMVITags assembles an MP4Box "-itags" string. MP4Box separates tags with
// ":" and key/value with "=", so colons inside values are stripped to avoid
// corrupting the tag list.
func buildMVITags(mv applemusic.MusicVideo, coverPath string) string {
	add := func(tags []string, key, value string) []string {
		value = strings.TrimSpace(value)
		if value == "" {
			return tags
		}
		value = strings.NewReplacer(":", " ", "\n", " ", "\r", " ").Replace(value)
		return append(tags, key+"="+value)
	}
	tags := []string{"tool="}
	tags = add(tags, "artist", mv.ArtistName)
	tags = add(tags, "title", mv.Name)
	tags = add(tags, "album", mv.AlbumName)
	tags = add(tags, "album_artist", mv.ArtistName)
	tags = add(tags, "performer", mv.ArtistName)
	tags = add(tags, "genre", firstGenre(mv.GenreNames))
	tags = add(tags, "created", mv.ReleaseDate)
	tags = add(tags, "ISRC", mv.ISRC)
	tags = add(tags, "UPC", mv.UPC)
	tags = add(tags, "copyright", mv.Copyright)
	if mv.TrackNumber > 0 {
		tags = add(tags, "track", strconv.Itoa(mv.TrackNumber))
		tags = add(tags, "tracknum", strconv.Itoa(mv.TrackNumber))
	}
	if mv.DiscNumber > 0 {
		tags = add(tags, "disk", strconv.Itoa(mv.DiscNumber))
	}
	switch strings.ToLower(mv.ContentRating) {
	case "explicit":
		tags = add(tags, "rating", "1")
	case "clean":
		tags = add(tags, "rating", "2")
	default:
		tags = add(tags, "rating", "0")
	}
	if coverPath != "" {
		tags = add(tags, "cover", coverPath)
	}
	return strings.Join(tags, ":")
}
