package media

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"amdl/internal/applemusic"
	"amdl/internal/config"
	"amdl/internal/jobs"
	"amdl/internal/limits"
	"amdl/internal/usertoken"
	"amdl/internal/wrapper"
)

// Downloader runs the per-track pipeline for the job manager. One instance is
// shared by every job; each job works on a withConfig clone bound to its
// effective config.
type Downloader struct {
	// store is the live runtime config; every ProcessJob/ValidateRequest call
	// reads a fresh snapshot from it. cfg is the effective config the current
	// operation runs under (snapshot plus the job's overrides — see
	// withConfig); it doubles as the fallback when store is nil in unit tests
	// that build a Downloader literal around a fixed config.
	store *config.Store
	cfg   config.Config
	// tokens picks the media-user-token for playlists and stations; nil
	// uses catalog.media_user_token alone.
	tokens  *usertoken.Provider
	catalog downloaderCatalog
	wrapper downloaderWrapper
	tools   *ToolChecker
	http    *http.Client
	mp4     *MP4Processor
	covers  *coverCache
	logger  *slog.Logger
	// These limiters are created once by NewDownloader and deliberately remain
	// shared by every shallow withConfig clone, so limits apply across jobs.
	downloadLimit *limits.Semaphore
	decryptLimit  *limits.Semaphore
	inFlightLimit *limits.Semaphore
	requestGate   *limits.RequestGate
	// deviceManifests is shared like the limiters above: a manifest one job
	// asked the wrapper for serves every other job.
	deviceManifests *deviceManifestCache

	// Per-job suppression for standalone cover paths that were already handled
	// (written, unavailable, or exhausted). The state has its own mutex because
	// different cover paths are allowed to proceed concurrently.
	standaloneCoverState *standaloneCoverState

	// background tracks work a job starts but does not wait for (the
	// motion-artwork backfill). A pointer, so every withConfig clone adds to
	// the one Wait drains.
	background *sync.WaitGroup
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
	MotionArtworkViaWebToken(context.Context, string, string) (applemusic.MotionArtwork, error)
}

type downloaderWrapper interface {
	Status(context.Context) (wrapper.Status, error)
	M3U8(context.Context, string) (string, error)
	Lyrics(context.Context, string, wrapper.LyricsRequestOptions) (string, error)
	WebPlayback(context.Context, string) (string, error)
	NewDecryptSession(context.Context, string) (wrapper.DecryptSession, error)
	PrepareKeys(context.Context, string, []string) error
	License(context.Context, string, string, string) (string, error)
}

// NewDownloader builds the process-wide Downloader. It creates the shared media
// pools from the current config; changing their sizes needs a restart.
func NewDownloader(store *config.Store, tokens *usertoken.Provider, catalog *applemusic.CatalogClient, wrapperClient *wrapper.Client, tools *ToolChecker, logger *slog.Logger) *Downloader {
	cfg := store.Get()
	return &Downloader{
		store: store, cfg: cfg, tokens: tokens, catalog: catalog, wrapper: wrapperClient, tools: tools,
		http: newHTTPClient(), mp4: newMP4Processor(cfg), logger: logger,
		downloadLimit:   limits.NewSemaphore(cfg.Download.MaxParallelDownloads),
		decryptLimit:    limits.NewSemaphore(config.MaxParallelDecrypts),
		inFlightLimit:   limits.NewSemaphore(cfg.Download.MaxParallelDownloads + config.MaxParallelDecrypts),
		requestGate:     catalog.RequestGate(),
		deviceManifests: newDeviceManifestCache(),
		background:      &sync.WaitGroup{},
	}
}

// Wait blocks until the background work jobs started has finished, or ctx is
// done. Call it after the job manager has stopped, so no job starts more.
func (d *Downloader) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		d.background.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for background downloader work: %w", ctx.Err())
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
	clone.standaloneCoverState = nil
	return &clone
}

// ValidateRequest implements jobs.Processor: it parses url and checks that the
// wrapper serves its storefront (skipped in simulate mode).
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
		// wrapper or its supported-storefront list.
		return nil
	}
	status, err := d.wrapper.Status(ctx)
	if err != nil {
		message := fmt.Sprintf("cannot reach the wrapper: %v", err)
		return &jobs.RequestError{Code: "decryptor_unavailable", Message: message, Cause: err}
	}
	if len(status.Regions) == 0 && (!status.Ready || !status.LoggedIn) {
		return &jobs.RequestError{Code: "decryptor_unavailable", Message: "the wrapper is not ready"}
	}
	for _, region := range status.Regions {
		if strings.EqualFold(region, storefront) {
			if !status.Ready || !status.LoggedIn {
				return &jobs.RequestError{Code: "decryptor_unavailable", Message: "the wrapper is not ready"}
			}
			return nil
		}
	}
	message := fmt.Sprintf("storefront %q is not served by the wrapper's account (it serves: %s)", storefront, strings.Join(status.Regions, ", "))
	return &jobs.RequestError{
		Code: "unsupported_storefront", Message: message, Storefront: storefront,
		SupportedStorefronts: append([]string(nil), status.Regions...),
	}
}
