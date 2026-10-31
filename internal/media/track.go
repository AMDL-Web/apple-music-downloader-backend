package media

import (
	"context"
	"fmt"
	"runtime/debug"

	"amdl/internal/applemusic"
	"amdl/internal/config"
	"amdl/internal/domain"
	"amdl/internal/jobs"
	"amdl/internal/logging"
)

// trackTarget is where a track of a collection lands: the values the path
// templates expand besides the track's own metadata.
type trackTarget struct {
	storefront      string
	collectionType  applemusic.URLType
	collectionName  string
	collectionID    string
	collectionIndex int
	// folderArtist is the collection's grouping artist, used for {ArtistName}
	// in directory segments so all of an album's tracks share one folder.
	folderArtist string
}

func (t trackTarget) outputPath(cfg config.Config, song applemusic.Song, codec, quality string) string {
	return outputPath(cfg, song, t.collectionType, t.collectionIndex, t.folderArtist, t.collectionName, t.collectionID, codec, quality)
}

func (t trackTarget) coverDirs(cfg config.Config, song applemusic.Song) (albumDir, artistDir string) {
	return standaloneCoverDirs(cfg, song, t.collectionType, t.collectionIndex, t.folderArtist, t.collectionName, t.collectionID)
}

// trackRun carries one track through the pipeline: metadata and standalone
// covers, then each configured codec until one completes. The embedded cover
// and the lyrics are fetched once, by the first codec attempt whose output
// path is free, so a track already on disk is skipped without asking for
// either. It owns the item and every
// status, message and event the item publishes; the media backend does the
// I/O. A run ends with the item completed, skipped, failed or cancelled, except
// on shutdown, which leaves the item as it is for recovery.
type trackRun struct {
	d     *Downloader
	media mediaBackend
	job   domain.Job
	item  domain.JobItem
	// initial is the track as the collection resolve produced it; song is the
	// refreshed metadata the pipeline uses once resolveMetadata ran.
	initial  applemusic.Song
	song     applemusic.Song
	target   trackTarget
	metadata *trackMetadataResolver

	// base is the job's reporter; reporter is the progress gate in front of it.
	// Everything reports through the gate except publish itself.
	base     jobs.Reporter
	reporter jobs.Reporter
	gate     *progressGate
	// lastStatus and lastProgress are what the last item_progress published.
	lastStatus   domain.ItemStatus
	lastProgress domain.ItemProgress

	output outputLease
	// enriched is set once cover and lyrics hold what enrich fetched.
	enriched bool
	cover    []byte
	lyrics   trackLyrics
}

func (d *Downloader) newTrackRun(job domain.Job, item domain.JobItem, initial applemusic.Song, target trackTarget, metadata *trackMetadataResolver, reporter jobs.Reporter) *trackRun {
	r := &trackRun{
		d: d, media: d.backend(), job: job, item: item, initial: initial, target: target, metadata: metadata,
		base: reporter, lastProgress: domain.ItemProgress{Download: -1, Decrypt: -1},
	}
	r.gate = newProgressGate(reporter, progressEventInterval(d.cfg))
	r.gate.emit = r.publish
	r.reporter = r.gate
	return r
}

// run processes the track and writes its terminal state. A panic anywhere in
// the pipeline fails this item instead of the process.
func (r *trackRun) run(ctx context.Context) (err error) {
	defer r.output.release()
	defer func() {
		if p := recover(); p != nil {
			logging.FromContext(ctx, r.d.logger).Error("track pipeline panicked",
				"job_id", r.job.ID, "item_id", r.item.ID, "adam_id", r.item.AdamID,
				"panic", p, "stack", string(debug.Stack()))
			err = r.finish(ctx, fmt.Errorf("track pipeline panicked: %v", p))
		}
	}()
	return r.finish(ctx, r.process(ctx))
}

// finish turns the pipeline's outcome into the item's terminal state.
// Completed and skipped items were written where they finished. A cancelled
// job's item becomes cancelled; a shutdown leaves it untouched, so recovery
// resumes it; anything else fails it.
func (r *trackRun) finish(ctx context.Context, err error) error {
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil && isShutdown(ctx):
		return err
	case ctx.Err() != nil:
		// The cancelled snapshot below carries the latest meter values, so a
		// held one need not go out first on a context that cannot write it.
		r.gate.discard()
		r.d.markCancelled(ctx, r.base, r.job.ID, &r.item)
		return err
	}
	// A held meter value goes out first, as the item's last live state, then
	// the failure.
	r.gate.flush(ctx)
	r.item.Status = domain.ItemFailed
	r.item.Error = err.Error()
	if r.item.StatusMessage == "" || r.item.Attempt == 0 {
		r.item.StatusMessage = "Download failed: " + err.Error()
	}
	r.d.persistTerminal(ctx, r.reporter, &r.item, func() domain.Event {
		return domain.Event{JobID: r.job.ID, ItemID: r.item.ID, Type: domain.EventItemFailed, Message: err.Error(), Payload: domain.MarshalEventPayload(r.item, map[string]any{"message": err.Error()})}
	})
	return err
}

// publish persists the item and emits it as an item_progress event. The full
// JobItem rides in the payload so a client updates from the stream alone.
func (r *trackRun) publish(ctx context.Context) {
	r.lastStatus = r.item.Status
	r.lastProgress = r.item.Progress
	r.gate.emitted()
	_ = r.base.UpdateItem(ctx, &r.item)
	_ = r.base.Event(ctx, itemProgressEvent(r.job.ID, r.item))
}

// stageSetter returns the publishStage the pipeline reports through. Each call
// updates the item and decides whether to publish it.
//
// Two gates decide whether a call publishes, and both cover the DB write and
// the event together: intermediate progress has no durability value (an
// unfinished item is reset to queued on resume), so persisting it more finely
// than it is streamed would only serve REST polling.
//
// The value gate: publish when the status changed, a pipeline stage finished,
// or one of the two meters landed on a different whole percent. That is
// rounding, not a 1pp delta: 0.4% → 0.6% crosses because it rounds 0 → 1. It
// keeps one 32KB download chunk from becoming one SQLite UPDATE.
//
// The time gate (progressGate): a meter-only move additionally has to clear
// download.progress_event_interval_ms since the last publish, because the value
// gate alone puts no floor on spacing. Status changes and stage completions
// skip the time gate so no transition is ever delayed, and a suppressed meter
// value is flushed by progressGate.Event ahead of whatever event supersedes it.
func (r *trackRun) stageSetter(ctx context.Context) publishStage {
	return func(status domain.ItemStatus, message string, mutate func(*domain.ItemProgress)) {
		r.item.Status = status
		r.item.StatusMessage = message
		if mutate != nil {
			mutate(&r.item.Progress)
		}
		switch {
		case status != r.lastStatus || stagesChanged(r.lastProgress, r.item.Progress):
			r.publish(ctx)
		case metersChanged(r.lastProgress, r.item.Progress):
			if r.gate.allow() {
				r.publish(ctx)
			} else {
				r.gate.hold()
			}
		}
	}
}

func (r *trackRun) maxAttempts() int { return r.d.cfg.Download.MaxAttempts }

func (r *trackRun) process(ctx context.Context) error {
	set := r.stageSetter(ctx)
	set(domain.ItemResolving, "resolving metadata", nil)
	if err := r.resolveMetadata(ctx); err != nil {
		return err
	}
	r.saveStandaloneCovers(ctx)
	if err := r.media.requireTools(ctx); err != nil {
		return err
	}
	codecs, err := configuredCodecs(r.d.cfg.Download)
	if err != nil {
		return err
	}
	return r.runCodecs(ctx, codecs, set)
}

func (r *trackRun) resolveMetadata(ctx context.Context) error {
	d, item := r.d, &r.item
	attempts := clampAttempts(r.maxAttempts())
	song, metadataAttempts, err := retryValue(ctx, r.maxAttempts(), retryBackoff, func(attempt int) (applemusic.Song, error) {
		d.setItemAttempt(ctx, r.reporter, item, "metadata", attempt, attempts, fmt.Sprintf("Fetching track metadata (%d/%d)", attempt, attempts))
		return r.metadata.song(ctx, r.initial, r.target.collectionType)
	}, func(failure retryFailure) {
		d.setRetryFailure(ctx, r.reporter, item, "metadata", "metadata", failure)
		d.emitRetryEvent(ctx, r.reporter, r.job.ID, item, "metadata", "", failure)
	})
	if err != nil {
		return err
	}
	if metadataAttempts > 1 {
		d.emitRecoveredEvent(ctx, r.reporter, r.job.ID, item, "metadata", "", metadataAttempts)
	}
	r.song = song
	item.Title = song.Name
	item.Artist = song.ArtistName
	item.Album = song.AlbumName
	item.DurationMS = song.DurationInMillis
	item.ArtworkURL = firstNonEmpty(song.ArtworkURL, song.AlbumArtworkURL, item.ArtworkURL)
	item.HasLyrics = song.HasLyrics
	// The resolving stage is done. No event of its own: the next publish is
	// immediate and carries the flag with it.
	markResolved(&item.Progress)
	_ = r.reporter.UpdateItem(ctx, item)
	return nil
}

// saveStandaloneCovers writes the album and artist covers next to the audio.
// A failure is reported on the item and never stops the download.
func (r *trackRun) saveStandaloneCovers(ctx context.Context) {
	cfg := r.d.cfg.Download
	if !cfg.SaveAlbumCover && !cfg.SaveArtistCover {
		return
	}
	albumDir, artistDir := r.target.coverDirs(r.d.cfg, r.song)
	err := r.d.saveStandaloneCovers(ctx, r.song, r.target.collectionType, r.target.storefront, albumDir, artistDir)
	if err == nil || ctx.Err() != nil {
		return
	}
	r.item.StatusMessage = "Standalone cover save failed; continuing download: " + err.Error()
	_ = r.reporter.UpdateItem(ctx, &r.item)
	_ = r.reporter.Event(ctx, domain.Event{JobID: r.job.ID, ItemID: r.item.ID, Type: domain.EventStandaloneCoverFailed, Message: err.Error(), Payload: domain.MarshalEventPayload(r.item, map[string]any{"message": err.Error()})})
}

// fetchCover fetches the embedded cover. Exhausting its retries only drops the
// cover; the error it returns is the job's cancellation.
func (r *trackRun) fetchCover(ctx context.Context) error {
	d, item := r.d, &r.item
	if !d.cfg.Download.EmbedCover {
		return nil
	}
	urls := trackCoverURLs(r.song, r.target.collectionType)
	attempts := clampAttempts(r.maxAttempts())
	cover, coverAttempts, err := retryValue(ctx, r.maxAttempts(), retryBackoff, func(attempt int) ([]byte, error) {
		d.setItemAttempt(ctx, r.reporter, item, "cover", attempt, attempts, fmt.Sprintf("Fetching cover (%d/%d)", attempt, attempts))
		return r.media.coverArt(ctx, urls, d.cfg.Download.CoverFormat, d.cfg.Download.CoverSize)
	}, func(failure retryFailure) {
		d.setRetryFailure(ctx, r.reporter, item, "cover", "cover", failure)
		d.emitRetryEvent(ctx, r.reporter, r.job.ID, item, "cover", "", failure)
	})
	switch {
	case err != nil && ctx.Err() != nil:
		return ctx.Err()
	case err != nil:
		item.StatusMessage = "Cover fetch retries exhausted; continuing without embedded cover: " + err.Error()
		_ = r.reporter.UpdateItem(ctx, item)
	case coverAttempts > 1:
		d.emitRecoveredEvent(ctx, r.reporter, r.job.ID, item, "cover", "", coverAttempts)
	}
	r.cover = cover
	return nil
}

// runCodecs tries each configured codec until one completes or the item is
// skipped. A cancelled job stops the chain at once instead of walking every
// remaining codec on a dead context.
func (r *trackRun) runCodecs(ctx context.Context, codecs []string, set publishStage) error {
	var lastErr error
	for i := range codecs {
		done, err := r.runCodec(ctx, codecs, i, lastErr, set)
		if done {
			return err
		}
		if ctx.Err() != nil {
			return err
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no configured codec succeeded")
	}
	return lastErr
}

// enrich fetches the embedded cover and the lyrics the first time it runs and
// then calls resume, which puts the item's attempt fields back on the fetch
// phase their retries overwrote. Neither fetch fails the track, so the error
// is the job's cancellation, and a cancelled enrich runs again on the next
// attempt.
func (r *trackRun) enrich(ctx context.Context, resume func()) error {
	if r.enriched {
		return nil
	}
	if err := r.fetchCover(ctx); err != nil {
		return err
	}
	if err := r.fetchLyrics(ctx); err != nil {
		return err
	}
	r.enriched = true
	resume()
	return nil
}

// claimOutput locks outPath and reports whether it already exists, in which
// case the item is now skipped.
func (r *trackRun) claimOutput(ctx context.Context, outPath string) (bool, error) {
	if err := r.output.acquire(ctx, outPath); err != nil {
		return false, err
	}
	return r.d.handleExistingOutput(ctx, r.reporter, r.job, &r.item, outPath)
}
