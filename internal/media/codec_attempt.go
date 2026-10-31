package media

import (
	"context"
	"fmt"
	"strings"

	"amdl/internal/domain"
	"amdl/internal/logging"
)

// codecAttempt is one codec's state shared by its fetch and decrypt phases.
type codecAttempt struct {
	codec    string
	outPath  string
	skipped  bool
	enhanced selectedDownloadMedia
	aacLC    aacLCDownload
	// keys fetches the enhanced stream's keys across the fetch phase's
	// attempts until the codec is done.
	keys keyPrefetch
}

func (a *codecAttempt) releaseInFlight() {
	a.enhanced.release()
	a.aacLC.release()
	a.keys.end()
}

// runCodec runs one codec's fetch phase and decrypt phase, each with its own
// retry budget, so a decrypt failure never re-downloads bytes already fetched.
// done reports that the item reached a terminal state (completed or skipped).
func (r *trackRun) runCodec(ctx context.Context, codecs []string, index int, lastErr error, set publishStage) (done bool, err error) {
	d, item := r.d, &r.item
	codec := codecs[index]
	codecName := strings.ToUpper(codec)
	maxAttempts := r.maxAttempts()
	item.Codec = codec
	// Cleared so a client reading the snapshot mid-fallback never sees the
	// previous codec's quality paired with the new codec's name; setItemQuality
	// repopulates them once the new attempt's actual quality is known.
	item.BitDepth, item.SampleRate, item.Bitrate = 0, 0, 0
	// Same reasoning for the progress breakdown: the stages this codec has yet
	// to run must not still be reporting the previous codec's results.
	item.Progress.ResetForAttempt()
	if index > 0 {
		item.StatusMessage = fmt.Sprintf("Codec %s failed; falling back to %s", strings.ToUpper(codecs[index-1]), codecName)
		_ = r.reporter.UpdateItem(ctx, item)
		_ = r.reporter.Event(ctx, domain.Event{JobID: r.job.ID, ItemID: item.ID, Type: domain.EventCodecFallback, Phase: codec, Message: item.StatusMessage, Payload: domain.MarshalEventPayload(*item, map[string]any{
			"from_codec": codecs[index-1], "to_codec": codec, "reason": codecFailureReason(lastErr),
		})})
	}

	attempt := &codecAttempt{codec: codec}
	defer attempt.releaseInFlight()
	discardFailedOutput := func() {
		if r.output.held() && attempt.outPath != "" {
			r.media.discardOutput(attempt.outPath)
		}
	}

	// Fetch phase: acquire the still-encrypted media into the configured
	// memory or disk backing.
	_, fetchAttempts, fetchErr := retryValue(ctx, maxAttempts, retryBackoff, func(n int) (struct{}, error) {
		return struct{}{}, r.fetch(ctx, attempt, n, set)
	}, func(failure retryFailure) {
		discardFailedOutput()
		operation := codecName
		if isNonRetryableError(failure.Err) {
			operation = "select " + operation
		}
		d.setRetryFailure(ctx, r.reporter, item, "download", operation, failure)
		d.emitRetryEvent(ctx, r.reporter, r.job.ID, item, "download", codec, failure)
	})
	if attempt.skipped && fetchErr == nil {
		r.output.release()
		return true, nil
	}
	if fetchErr != nil {
		r.output.release()
		if ctx.Err() == nil {
			d.reportCodecFailed(ctx, r.reporter, r.job, *item, codec, "download", maxAttempts, fetchAttempts, fetchErr)
		}
		return false, fetchErr
	}
	if fetchAttempts > 1 {
		d.emitRecoveredEvent(ctx, r.reporter, r.job.ID, item, "download", codec, fetchAttempts)
	}

	// Decrypt phase: decrypt, remux, verify, tag and commit the bytes fetched
	// above. A retry re-runs only this phase.
	_, decryptAttempts, decryptErr := retryValue(ctx, maxAttempts, retryBackoff, func(n int) (struct{}, error) {
		attempts := clampAttempts(maxAttempts)
		d.setItemAttempt(ctx, r.reporter, item, "decrypt", n, attempts, fmt.Sprintf("Decrypting %s (%d/%d)", codecName, n, attempts))
		if codec == "aac-lc" {
			return struct{}{}, d.decryptAACLC(ctx, r.job, item, r.song, attempt.aacLC, r.lyrics, r.cover, attempt.outPath, r.reporter, set)
		}
		return struct{}{}, d.downloadEnhancedCodec(ctx, r.job, item, r.song, codec, r.lyrics, r.cover, attempt.outPath, attempt.enhanced, r.reporter, set)
	}, func(failure retryFailure) {
		discardFailedOutput()
		d.setRetryFailure(ctx, r.reporter, item, "decrypt", codecName, failure)
		d.emitRetryEvent(ctx, r.reporter, r.job.ID, item, "decrypt", codec, failure)
	})
	// Once the decrypt phase has returned, low memory mode's checkpoint is no
	// longer useful to this run: a decrypt error may mean corrupt bytes, and a
	// fallback codec fetches its own. An interrupted job keeps it: a cancelled
	// job's checkpoints go with the job, and a shutdown's resume the download.
	// High memory mode and AAC-LC have no rawPath.
	if ctx.Err() == nil {
		cleanupResumableDownload(attempt.enhanced.rawPath)
	}
	attempt.releaseInFlight()
	if decryptErr != nil {
		r.output.release()
		if ctx.Err() == nil {
			d.reportCodecFailed(ctx, r.reporter, r.job, *item, codec, "decrypt", maxAttempts, decryptAttempts, decryptErr)
		}
		return false, decryptErr
	}
	if decryptAttempts > 1 {
		d.emitRecoveredEvent(ctx, r.reporter, r.job.ID, item, "decrypt", codec, decryptAttempts)
	}

	switch {
	case index > 0:
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
	d.persistTerminal(ctx, r.reporter, item, func() domain.Event {
		return domain.Event{JobID: r.job.ID, ItemID: item.ID, Type: domain.EventItemCompleted, Message: item.StatusMessage, Payload: domain.MarshalEventPayload(*item, map[string]any{
			"codec": codec, "download_attempts": fetchAttempts, "decrypt_attempts": decryptAttempts,
			"max_attempts": clampAttempts(maxAttempts), "fallback_from": fallbackCodec(codecs, index),
		})}
	})
	if index > 0 {
		// The one line that says the output is not what was asked for. Warn,
		// not Info: the item itself completed, so nothing else in the log
		// distinguishes a degraded track from a clean one.
		logging.FromContext(ctx, d.logger).Warn("track completed on fallback codec",
			"item_id", item.ID, "adam_id", item.AdamID, "title", item.Title,
			"codec", codec, "fallback_from", fallbackCodec(codecs, index))
	}
	r.output.release()
	return true, nil
}

// fetch is one attempt of a codec's fetch phase: pick the stream, take the
// output lock, stop if the output already exists, fetch the cover and lyrics
// if no earlier attempt did, then transfer.
func (r *trackRun) fetch(ctx context.Context, attempt *codecAttempt, n int, set publishStage) error {
	d, item := r.d, &r.item
	codecName := strings.ToUpper(attempt.codec)
	attempts := clampAttempts(r.maxAttempts())
	if attempt.codec == "aac-lc" {
		d.setItemAttempt(ctx, r.reporter, item, "download", n, attempts, fmt.Sprintf("Downloading %s (%d/%d)", codecName, n, attempts))
		// No per-track manifest to read quality from for aac-lc (unlike the
		// enhanced codecs' HLS variant); leave bit_depth/sample_rate/bitrate at
		// 0 (omitted from the API response) rather than assert a value.
		attempt.outPath = r.target.outputPath(d.cfg, r.song, attempt.codec, aacLCQualityLabel)
		if skip, err := r.claimOutput(ctx, attempt.outPath); skip || err != nil {
			attempt.skipped = skip
			return err
		}
		if err := r.enrich(ctx, func() {
			d.setItemAttempt(ctx, r.reporter, item, "download", n, attempts, fmt.Sprintf("Downloading %s (%d/%d)", codecName, n, attempts))
		}); err != nil {
			return err
		}
		download, err := d.fetchAACLCMedia(ctx, r.job, item, r.song, r.reporter, set)
		if err != nil {
			return err
		}
		attempt.aacLC = download
		return nil
	}
	d.setItemAttempt(ctx, r.reporter, item, "download", n, attempts, fmt.Sprintf("Selecting %s (%d/%d)", codecName, n, attempts))
	selected, err := d.selectEnhancedMedia(ctx, r.job, item, r.song, attempt.codec, r.reporter, set)
	if err != nil {
		return err
	}
	attempt.outPath = r.target.outputPath(d.cfg, r.song, attempt.codec, qualityLabel(selected.info))
	if skip, err := r.claimOutput(ctx, attempt.outPath); skip || err != nil {
		attempt.skipped = skip
		return err
	}
	if err := r.enrich(ctx, func() {
		d.setItemAttempt(ctx, r.reporter, item, "download", n, attempts, fmt.Sprintf("Downloading %s (%d/%d)", codecName, n, attempts))
	}); err != nil {
		return err
	}
	selected, err = d.downloadSelectedEnhancedMedia(ctx, selected, attempt.codec, r.job.ID, attempt.outPath, &attempt.keys, set)
	if err != nil {
		return err
	}
	attempt.enhanced = selected
	return nil
}
