package media

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"amdl/internal/applemusic"
	"amdl/internal/domain"
	"amdl/internal/jobs"
	"amdl/internal/logging"
)

// selectedDownloadMedia is one enhanced codec's chosen stream and, after the
// fetch phase, its encrypted media.
type selectedDownloadMedia struct {
	info selectedMediaInfo
	// songID is the adamId the stream's keys are fetched for.
	songID string
	// estimatedBytes is the size info's bandwidth predicts for the track.
	estimatedBytes int64
	encryptedMedia
}

func (d *Downloader) selectEnhancedMedia(ctx context.Context, job domain.Job, item *domain.JobItem, song applemusic.Song, codec string, reporter jobs.Reporter, set publishStage) (selectedDownloadMedia, error) {
	set(domain.ItemResolving, fmt.Sprintf("Selecting %s media stream", strings.ToUpper(codec)), nil)
	info, err := d.selectEnhancedStream(ctx, job.Storefront, song, codec)
	if err != nil {
		return selectedDownloadMedia{}, err
	}
	d.setItemQuality(ctx, reporter, item, info.BitDepth, info.SampleRate, info.Bandwidth)
	_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: domain.EventCodecSelected, Phase: codec, Payload: domain.MarshalEventPayload(*item, map[string]any{
		"codec_id": info.CodecID, "bit_depth": info.BitDepth, "sample_rate": info.SampleRate, "bitrate": info.Bandwidth,
		"attempt": item.Attempt, "max_attempts": item.MaxAttempts,
	})})

	return selectedDownloadMedia{info: info, songID: song.ID, estimatedBytes: estimatedMediaBytes(song.DurationInMillis, info.Bandwidth)}, nil
}

// selectEnhancedStream is the download boundary between media discovery and
// transfer. Quality inventory shares its source resolver and variant selector,
// but stops at the master playlist instead of validating a concrete media
// playlist here. It runs for real in simulate mode too.
func (d *Downloader) selectEnhancedStream(ctx context.Context, storefront string, song applemusic.Song, codec string) (selectedMediaInfo, error) {
	master, cached, err := d.resolveEnhancedHLS(ctx, storefront, song, codec)
	if err != nil {
		return selectedMediaInfo{}, err
	}
	extract := func(master string) (selectedMediaInfo, error) {
		return extractMedia(ctx, d.http, master, codec, d.cfg.Download.ALACMaxSampleRate, d.cfg.Download.ALACMaxBitDepth, d.requestGate)
	}
	info, err := extract(master)
	var notFound codecNotFoundError
	if err != nil && ctx.Err() == nil && !errors.As(err, &notFound) {
		// The device manifest URL is not known to have gone stale, but a
		// playlist that will not load is reason enough to ask the wrapper again.
		key := deviceManifestKey{storefront: storefront, songID: song.ID}
		d.deviceManifests.forget(key)
		if cached {
			// A remembered URL may lead to an asset Apple has since replaced:
			// try the wrapper's current answer once before giving up.
			if fresh, _, resolveErr := d.resolveEnhancedHLS(ctx, storefront, song, codec); resolveErr == nil && fresh != master {
				info, err = extract(fresh)
			}
		}
	}
	if err != nil {
		return selectedMediaInfo{}, fmt.Errorf("select %s media: %w", codec, err)
	}
	return info, nil
}

// webALACMaxSampleRate is the highest ALAC sample rate the web player's master
// playlist is relied on to list. Above it the playlist can leave renditions out.
const webALACMaxSampleRate = 48000

// wantsDeviceManifest reports whether codec's rendition for song may be missing
// from the web player's master playlist. In the wrapper's device manifest for
// the same song the 88.2, 96 and 192 kHz ALAC renditions that the web
// playlist sometimes omits are always listed, and nothing in the web playlist
// says when it is short. Atmos and AAC renditions match in both, so only a
// hi-res-lossless track whose ALAC may exceed 48 kHz is worth a wrapper
// request: /m3u8 shares one lock with /key. A ceiling of 0 or less means no
// ceiling (selectVariant), so it may exceed 48 kHz too.
func (d *Downloader) wantsDeviceManifest(song applemusic.Song, codec string) bool {
	if !song.HiResLossless || codec != "alac" {
		return false
	}
	maxRate := d.cfg.Download.ALACMaxSampleRate
	return maxRate <= 0 || maxRate > webALACMaxSampleRate
}

// resolveEnhancedHLS chooses the master-playlist source shared by downloads
// and quality inventory. The caller decides whether to continue through a
// concrete media playlist (download) or only inspect the master variants
// (quality query). A song without a manifest wraps
// applemusic.ErrNoEnhancedHLS, which is not retryable, so the codec loop falls
// back at once instead of spending its retry budget.
//
// A track for which wantsDeviceManifest holds takes the device manifest, kept
// per (storefront, song) by deviceManifests, and keeps the catalog's enhancedHls
// only if the wrapper cannot supply one. cached reports that the returned URL
// came from that cache, so a caller whose fetch fails can drop it.
func (d *Downloader) resolveEnhancedHLS(ctx context.Context, storefront string, song applemusic.Song, codec string) (master string, cached bool, err error) {
	master = song.EnhancedHLS
	if d.wantsDeviceManifest(song, codec) {
		key := deviceManifestKey{storefront: storefront, songID: song.ID}
		if url, ok := d.deviceManifests.get(key); ok {
			return url, true, nil
		}
		m3u8, err := d.wrapper.M3U8(ctx, song.ID)
		if err == nil && m3u8 == "" {
			err = errors.New("empty manifest")
		}
		if err == nil {
			d.deviceManifests.put(key, m3u8)
			return m3u8, false, nil
		}
		if ctx.Err() != nil {
			return "", false, fmt.Errorf("request device m3u8: %w", err)
		}
		logging.FromContext(ctx, d.logger).Warn("device manifest unavailable; using the web manifest, which may lack hi-res renditions",
			"song_id", song.ID, "error", err)
	}
	if master == "" {
		return "", false, fmt.Errorf("song %s: %w", song.ID, applemusic.ErrNoEnhancedHLS)
	}
	return master, false, nil
}

// downloadSelectedEnhancedMedia is the enhanced codec's fetch phase: it waits
// for the in-flight and download permits and transfers the still-encrypted
// media, fetching the stream's keys in the background through keys once it
// holds the in-flight permit. The in-flight permit is returned held, in
// selected.releaseInFlight, until decryption has consumed the bytes.
func (d *Downloader) downloadSelectedEnhancedMedia(ctx context.Context, selected selectedDownloadMedia, codec, jobID, outPath string, keys *keyPrefetch, set publishStage) (selectedDownloadMedia, error) {
	codecName := strings.ToUpper(codec)
	onProgress := func(done, total int64) {
		if total <= 0 {
			return // Content-Length unknown; the meter stays at 0
		}
		p := clampFrac(float64(done) / float64(total))
		set(domain.ItemDownloading, fmt.Sprintf("%s download %.0f%%", codecName, p*100), downloadProgress(done, p))
	}
	set(domain.ItemWaitingDownload, "waiting for download slot", nil)
	releaseInFlight, err := d.inFlightLimit.Acquire(ctx)
	if err != nil {
		return selectedDownloadMedia{}, err
	}
	keys.start(ctx, d, selected.songID, selected.info.Keys)
	releaseDownload, err := d.downloadLimit.Acquire(ctx)
	if err != nil {
		releaseInFlight()
		return selectedDownloadMedia{}, err
	}
	set(domain.ItemDownloading, fmt.Sprintf("Downloading %s encrypted media", codecName), startDownload)
	media, err := func() (encryptedMedia, error) {
		defer releaseDownload()
		return d.backend().fetch(ctx, fetchRequest{
			codec: codec, uri: selected.info.MediaURI, jobID: jobID, outPath: outPath,
			estimatedBytes: selected.estimatedBytes,
		}, onProgress)
	}()
	if err != nil {
		releaseInFlight()
		return selectedDownloadMedia{}, fmt.Errorf("download encrypted media: %w", err)
	}
	media.releaseInFlight = releaseInFlight
	selected.encryptedMedia = media
	return selected, nil
}

// keyPrefetch is one codec attempt's background fetch of its stream's keys.
// wrapper-lite answers /key one at a time, about two seconds each, so fetching
// them only after the download would line an album's tracks up behind one
// another at the decrypt stage; fetched during it, the wait overlaps the
// transfers. It belongs to the codec attempt, not to one transfer: a failed
// transfer leaves the fetch running for the retry, which needs the same keys
// and would otherwise queue again at the back. It starts only once the track
// holds an in-flight permit, so at most that many tracks prefetch at a time.
// A failure is logged and left to the decrypt phase, which asks again. It is
// used from the track's own goroutine only.
type keyPrefetch struct {
	keys []string
	stop context.CancelFunc
}

// start fetches keys for songID in the background unless that fetch already
// runs. A retry that chose a stream with other keys ends the old fetch.
func (p *keyPrefetch) start(ctx context.Context, d *Downloader, songID string, keys []string) {
	if len(keys) == 0 || (p.stop != nil && slices.Equal(p.keys, keys)) {
		return
	}
	p.end()
	ctx, cancel := context.WithCancel(ctx)
	p.keys, p.stop = keys, cancel
	backend, log := d.backend(), logging.FromContext(ctx, d.logger)
	go func() {
		if err := backend.prefetchKeys(ctx, songID, keys); err != nil && ctx.Err() == nil {
			log.Warn("key prefetch failed; the decrypt stage asks again", "song_id", songID, "error", err)
		}
	}()
}

// end stops the fetch if it still runs. Keys already fetched stay cached.
func (p *keyPrefetch) end() {
	if p.stop != nil {
		p.stop()
		p.stop = nil
	}
}

// downloadEnhancedCodec is the enhanced codec's decrypt phase: it decrypts the
// fetched media one fragment at a time straight into ffmpeg, then verifies,
// tags and commits the result (finalizeTrackOutput). Both memory modes stream;
// only the encrypted input's backing differs.
func (d *Downloader) downloadEnhancedCodec(ctx context.Context, job domain.Job, item *domain.JobItem, song applemusic.Song, codec string, lyrics trackLyrics, cover []byte, outPath string, selected selectedDownloadMedia, reporter jobs.Reporter, set publishStage) error {
	backend := d.backend()
	input, inputSize, closeInput, err := selected.open()
	if err != nil {
		return fmt.Errorf("open downloaded media: %w", err)
	}
	defer closeInput()

	staged, cleanupStaged, err := backend.stage(selected.encryptedMedia)
	if err != nil {
		return fmt.Errorf("create flatten output: %w", err)
	}
	defer cleanupStaged()

	// Wait for the keys before taking a decrypt slot, so a track counts as
	// decrypting only while it decrypts. This usually joins the fetch the
	// transfer started. A failed key is left to the session, which fetches the
	// keys the fragments use and reports what went wrong.
	set(domain.ItemWaitingDecrypt, "waiting for decryption key", nil)
	_ = backend.prepareKeys(ctx, song.ID, selected.info.Keys)
	if err := ctx.Err(); err != nil {
		return err
	}
	set(domain.ItemWaitingDecrypt, "waiting for decrypt slot", nil)
	releaseDecrypt, err := d.decryptLimit.Acquire(ctx)
	if err != nil {
		return err
	}
	set(domain.ItemDecrypting, "opening decrypt session", startDecrypt)
	err = func() error {
		defer releaseDecrypt()
		return backend.decrypt(ctx, decryptRequest{
			codec: codec, songID: song.ID, input: input, size: inputSize, staged: staged, keys: selected.info.Keys,
		}, decryptHooks{
			ready: func() { set(domain.ItemDecrypting, "decrypting", nil) },
			// The decrypt meter tracks encrypted bytes consumed; the total
			// sample count isn't known until the last fragment is read.
			progress: func(p float64) {
				set(domain.ItemDecrypting, fmt.Sprintf("decrypting %.0f%%", clampFrac(p)*100), decryptFrac(p))
			},
			// The decrypted input has reached EOF and ffmpeg is now flushing the
			// progressive MP4 trailer. Publish remuxing inside that real window.
			// Decrypt is done by definition here, whether or not the meter got
			// to say so.
			afterInput: func() { set(domain.ItemRemuxing, "remuxing", decryptFrac(1)) },
		})
	}()
	if err != nil {
		return err
	}
	set(domain.ItemRemuxing, "remuxed", markRemuxed)
	selected.release()

	return d.finalizeTrackOutput(ctx, backend, trackOutput{
		job: job, item: item, reporter: reporter, song: song, codec: codec,
		lyrics: lyrics, cover: cover, staged: staged, outPath: outPath,
		labels: enhancedOutputLabels, allowALACRepair: codec == "alac",
	}, set)
}
