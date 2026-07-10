package media

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"

	"amdl/internal/applemusic"
	"amdl/internal/domain"
	"amdl/internal/jobs"
)

// simulateTrack replays the lifecycle of processTrack — identical statuses,
// progress bands, status messages, DB fields, and events — without touching
// the wrapper, ffmpeg, or the disk. Media selection runs for real (the
// enhanced HLS manifest is fetched and parsed, so codec_selected events and
// the item's quality fields carry real values, and selection failures fall
// back through the configured codecs exactly like a real download); only the
// encrypted transfer, decryption, remux, and disk writes are simulated. The
// transfer phases are paced by a random speed drawn from the configured
// simulate speed range.
func (d *Downloader) simulateTrack(ctx context.Context, job domain.Job, item *domain.JobItem, song applemusic.Song, collectionType applemusic.URLType, collectionName, collectionID string, playlistIndex int, folderArtist string, reporter jobs.Reporter, set func(domain.ItemStatus, float64, string)) error {
	maxAttempts := clampAttempts(d.cfg.Download.MaxAttempts)
	if d.cfg.Download.EmbedCover {
		d.setItemAttempt(ctx, reporter, item, "cover", 1, maxAttempts, fmt.Sprintf("Fetching cover (1/%d)", maxAttempts))
	}
	if (d.cfg.Download.EmbedLyrics || d.cfg.Download.SaveLyricsFile) && song.HasLyrics {
		d.setItemAttempt(ctx, reporter, item, "lyrics", 1, maxAttempts, fmt.Sprintf("Fetching lyrics (1/%d)", maxAttempts))
	}

	codecs, err := configuredCodecs(d.cfg.Download)
	if err != nil {
		return d.failItem(ctx, reporter, job, *item, err)
	}
	// Mirrors handleExistingOutput without ever mutating the disk: simulate
	// mode must not delete a previously real-downloaded file even under force.
	existingSkip := func(outPath string) bool {
		item.OutputPath = outPath
		if _, statErr := os.Stat(outPath); statErr == nil && !job.Force {
			item.Status = domain.ItemSkipped
			item.Progress = 1
			item.RetryKind = ""
			item.Attempt = 0
			item.MaxAttempts = 0
			item.StatusMessage = "File already exists; skipped"
			_ = reporter.UpdateItem(ctx, *item)
			_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "item_skipped", Message: "already exists"})
			return true
		}
		if job.Force {
			// The real path deletes stale outputs here; simulate mode only
			// emits the same event and never touches the disk.
			_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "item_overwrite", Message: "force overwrite enabled"})
		}
		return false
	}
	var lastErr error
	for codecIndex, codec := range codecs {
		codecName := strings.ToUpper(codec)
		if codecIndex > 0 {
			item.StatusMessage = fmt.Sprintf("Codec %s failed; falling back to %s", strings.ToUpper(codecs[codecIndex-1]), codecName)
			_ = reporter.UpdateItem(ctx, *item)
			_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "codec_fallback", Phase: codec, Message: item.StatusMessage, Payload: marshalPayload(map[string]any{
				"from_codec": codecs[codecIndex-1], "to_codec": codec, "reason": codecFailureReason(lastErr),
			})})
		}
		item.Codec = codec
		item.BitDepth, item.SampleRate, item.Bitrate = 0, 0, 0

		var info selectedMediaInfo
		var outPath string
		fetchAttempts := 1
		if codec == "aac-lc" {
			// Like the real AAC-LC path, the existing-output check runs before
			// any WebPlayback traffic or codec_selected event. The playlist
			// itself comes from wrapper.WebPlayback, which test mode must not
			// depend on, so the selection is faked with the same event.
			d.setItemAttempt(ctx, reporter, item, "download", 1, maxAttempts, fmt.Sprintf("Downloading %s (1/%d)", codecName, maxAttempts))
			outPath = outputPath(d.cfg, song, collectionType, playlistIndex, folderArtist, collectionName, collectionID, codec, "256Kbps")
			if existingSkip(outPath) {
				return nil
			}
			set(domain.ItemDownloading, 0.03, "requesting AAC-LC WebPlayback asset")
			_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "codec_selected", Phase: "aac-lc", Payload: marshalPayload(map[string]any{
				"codec_id": "aac-lc", "attempt": item.Attempt, "max_attempts": item.MaxAttempts,
			})})
			info = selectedMediaInfo{CodecID: "aac-lc", Bandwidth: 256000}
		} else {
			// Selection is a real network operation, so it keeps the real
			// path's retry envelope: per-attempt messages, operation_retry
			// events, and recovery events with true attempt counts.
			codecMaxAttempts := attemptsForCodec(d.cfg.Download.MaxAttempts, codecIndex)
			var selected selectedDownloadMedia
			_, attempts, selectErr := retryValue(ctx, codecMaxAttempts, retryBackoff, func(attempt int) (struct{}, error) {
				d.setItemAttempt(ctx, reporter, item, "download", attempt, clampAttempts(codecMaxAttempts), fmt.Sprintf("Selecting %s (%d/%d)", codecName, attempt, clampAttempts(codecMaxAttempts)))
				s, err := d.selectEnhancedMedia(ctx, job, item, song, codec, reporter, set)
				if err != nil {
					return struct{}{}, err
				}
				selected = s
				return struct{}{}, nil
			}, func(failure retryFailure) {
				operation := codecName
				if isNonRetryableError(failure.Err) {
					operation = "select " + operation
				}
				d.setRetryFailure(ctx, reporter, item, "download", operation, failure)
				d.emitRetryEvent(ctx, reporter, job.ID, item.ID, "download", codec, failure)
			})
			fetchAttempts = attempts
			if selectErr != nil {
				lastErr = selectErr
				d.reportCodecFailed(ctx, reporter, job, *item, codec, "download", codecMaxAttempts, attempts, selectErr)
				continue
			}
			if attempts > 1 {
				d.emitRecoveredEvent(ctx, reporter, job.ID, item.ID, "download", codec, attempts)
			}
			info = selected.info
			outPath = outputPath(d.cfg, song, collectionType, playlistIndex, folderArtist, collectionName, collectionID, codec, qualityLabel(info))
			if existingSkip(outPath) {
				return nil
			}
		}

		totalBytes := simulatedSizeBytes(song, info)
		var transferErr error
		if codec == "aac-lc" {
			set(domain.ItemDownloading, 0.05, "downloading encrypted AAC-LC media")
			transferErr = d.simulateTransfer(ctx, totalBytes, func(p float64) {
				set(domain.ItemDownloading, 0.05+p*0.50, fmt.Sprintf("downloading %.0f%%", p*100))
			})
		} else {
			set(domain.ItemDownloading, 0.05, fmt.Sprintf("Downloading %s encrypted media", codecName))
			transferErr = d.simulateTransfer(ctx, totalBytes, func(p float64) {
				set(domain.ItemDownloading, 0.05+p*0.50, fmt.Sprintf("%s download %.0f%%", codecName, p*100))
			})
		}
		if transferErr != nil {
			return d.failItem(ctx, reporter, job, *item, transferErr)
		}

		d.setItemAttempt(ctx, reporter, item, "decrypt", 1, maxAttempts, fmt.Sprintf("Decrypting %s (1/%d)", codecName, maxAttempts))
		if err := d.simulateDecryptPhases(ctx, song, codec, info.SampleRate, totalBytes, set); err != nil {
			return d.failItem(ctx, reporter, job, *item, err)
		}

		item.Status = domain.ItemCompleted
		item.Progress = 1
		item.OutputPath = outPath
		item.Codec = codec
		switch {
		case codecIndex > 0:
			item.StatusMessage = fmt.Sprintf("Completed after fallback to %s", codecName)
		case fetchAttempts > 1:
			item.StatusMessage = fmt.Sprintf("%s completed (download took %d attempts)", codecName, fetchAttempts)
		default:
			item.StatusMessage = fmt.Sprintf("%s download completed", codecName)
		}
		_ = reporter.UpdateItem(ctx, *item)
		_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "item_completed", Message: item.StatusMessage, Payload: marshalPayload(map[string]any{
			"codec": codec, "download_attempts": fetchAttempts, "decrypt_attempts": 1,
			"max_attempts": maxAttempts, "fallback_from": fallbackCodec(codecs, codecIndex),
		})})
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no configured codec succeeded")
	}
	return d.failItem(ctx, reporter, job, *item, lastErr)
}

// simulateDecryptPhases walks the post-download statuses with the same
// progress values and messages the real decrypt path emits for the codec.
func (d *Downloader) simulateDecryptPhases(ctx context.Context, song applemusic.Song, codec string, sampleRate int, totalBytes int64, set func(domain.ItemStatus, float64, string)) error {
	if codec == "aac-lc" {
		// decryptAACLC reports no granular progress between the license
		// request and the save step.
		for _, step := range []struct {
			status  domain.ItemStatus
			prog    float64
			message string
			pause   time.Duration
		}{
			{domain.ItemDecrypting, 0.55, "acquiring Widevine license", 400 * time.Millisecond},
			{domain.ItemSaving, 0.94, "saving AAC-LC", 150 * time.Millisecond},
			{domain.ItemTagging, 0.97, "writing AAC-LC metadata", 200 * time.Millisecond},
		} {
			set(step.status, step.prog, step.message)
			if err := simulatePause(ctx, step.pause); err != nil {
				return err
			}
		}
		return nil
	}
	set(domain.ItemDecrypting, 0.55, "extracting samples")
	totalSamples := simulatedSampleCount(song, codec, sampleRate)
	// Decrypt works on bytes already in memory, so pace it faster than the
	// network transfer: the same speed range over roughly a third of the size.
	if err := d.simulateTransfer(ctx, totalBytes/3, func(p float64) {
		done := int(p * float64(totalSamples))
		if done > totalSamples {
			done = totalSamples
		}
		set(domain.ItemDecrypting, 0.55+p*0.35, fmt.Sprintf("decrypting %d/%d samples", done, totalSamples))
	}); err != nil {
		return err
	}
	for _, step := range []struct {
		status  domain.ItemStatus
		prog    float64
		message string
		pause   time.Duration
	}{
		{domain.ItemRemuxing, 0.90, "remuxing", 300 * time.Millisecond},
		{domain.ItemSaving, 0.94, "saving", 150 * time.Millisecond},
		{domain.ItemTagging, 0.97, "writing metadata", 200 * time.Millisecond},
	} {
		set(step.status, step.prog, step.message)
		if err := simulatePause(ctx, step.pause); err != nil {
			return err
		}
	}
	return nil
}

// simulateTransfer advances a fake transfer of totalBytes, re-rolling the
// speed inside [min_speed_kbps, max_speed_kbps] every tick and reporting
// completion in [0,1] through progress.
func (d *Downloader) simulateTransfer(ctx context.Context, totalBytes int64, progress func(float64)) error {
	if totalBytes <= 0 {
		totalBytes = 1
	}
	minSpeed := d.cfg.Simulate.MinSpeedKBps
	if minSpeed < 1 {
		minSpeed = 1
	}
	maxSpeed := d.cfg.Simulate.MaxSpeedKBps
	if maxSpeed < minSpeed {
		maxSpeed = minSpeed
	}
	const tick = 200 * time.Millisecond
	var done float64
	for done < float64(totalBytes) {
		if err := simulatePause(ctx, tick); err != nil {
			return err
		}
		speed := float64(minSpeed)
		if maxSpeed > minSpeed {
			speed += rand.Float64() * float64(maxSpeed-minSpeed)
		}
		done += speed * 1024 * tick.Seconds()
		p := done / float64(totalBytes)
		if p > 1 {
			p = 1
		}
		progress(p)
	}
	return nil
}

func simulatePause(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func simulatedSizeBytes(song applemusic.Song, info selectedMediaInfo) int64 {
	seconds := float64(song.DurationInMillis) / 1000
	if seconds <= 0 {
		seconds = 210
	}
	bandwidth := info.Bandwidth
	if bandwidth <= 0 {
		bandwidth = 256000
	}
	return int64(float64(bandwidth) / 8 * seconds)
}

// simulatedSampleCount estimates the packet count of the decrypted stream
// from the track duration and the sample rate reported by the selected
// manifest (falling back to 44.1 kHz when the manifest carries none).
func simulatedSampleCount(song applemusic.Song, codec string, sampleRate int) int {
	seconds := float64(song.DurationInMillis) / 1000
	if seconds <= 0 {
		seconds = 210
	}
	if sampleRate <= 0 {
		sampleRate = 44100
	}
	framesPerPacket := 1024.0 // AAC-family samples per packet
	if codec == "alac" {
		framesPerPacket = 4096
	}
	count := int(seconds * float64(sampleRate) / framesPerPacket)
	if count < 1 {
		count = 1
	}
	return count
}
