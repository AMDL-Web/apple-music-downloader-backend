package media

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"time"

	"amdl/internal/applemusic"
	"amdl/internal/config"
	"amdl/internal/domain"
	"amdl/internal/jobs"
	"amdl/internal/limits"
	"amdl/internal/wrapper"
)

// newTestDownloader completes a Downloader literal with the process-wide
// pieces NewDownloader creates: media pools sized from the literal's config, the
// device manifest cache and the background-work tracker. Pools a test set itself are kept.
func newTestDownloader(d *Downloader) *Downloader {
	cfg := d.cfg
	if d.store != nil {
		cfg = d.store.Get()
	}
	if d.downloadLimit == nil {
		d.downloadLimit = limits.NewSemaphore(cfg.Download.MaxParallelDownloads)
	}
	if d.decryptLimit == nil {
		d.decryptLimit = limits.NewSemaphore(config.MaxParallelDecrypts)
	}
	if d.inFlightLimit == nil {
		d.inFlightLimit = limits.NewSemaphore(cfg.Download.MaxParallelDownloads + config.MaxParallelDecrypts)
	}
	if d.background == nil {
		d.background = &sync.WaitGroup{}
	}
	if d.deviceManifests == nil {
		d.deviceManifests = newDeviceManifestCache()
	}
	if d.mp4 == nil {
		d.mp4 = newMP4Processor(cfg)
	}
	return d
}

// withDecryptLimit sizes d's decrypt pool to n instead of
// config.MaxParallelDecrypts, and the in-flight pool spanning transfers and
// decrypts to match, as NewDownloader would for that size.
func withDecryptLimit(d *Downloader, n int) *Downloader {
	d.decryptLimit = limits.NewSemaphore(n)
	d.inFlightLimit = limits.NewSemaphore(d.cfg.Download.MaxParallelDownloads + n)
	return d
}

// newTestQualityService builds a QualityService around fakes.
func newTestQualityService(cfg config.Config, catalog downloaderCatalog, wrapperClient downloaderWrapper) *QualityService {
	return NewQualityService(newTestDownloader(&Downloader{cfg: cfg, catalog: catalog, wrapper: wrapperClient, http: newHTTPClient()}))
}

// processTrack runs one track through the pipeline the way processJob does,
// with its own metadata resolver.
func (d *Downloader) processTrack(ctx context.Context, job domain.Job, item domain.JobItem, initial applemusic.Song, storefront string, collectionType applemusic.URLType, collectionName, collectionID string, collectionIndex int, folderArtist string, reporter jobs.Reporter) error {
	target := trackTarget{
		storefront: storefront, collectionType: collectionType, collectionName: collectionName,
		collectionID: collectionID, collectionIndex: collectionIndex, folderArtist: folderArtist,
	}
	return d.newTrackRun(job, item, initial, target, newTrackMetadataResolver(d, storefront), reporter).run(ctx)
}

type fakeDownloaderWrapper struct {
	status    wrapper.Status
	lyrics    string
	lyricsErr error
	// lyricsFor, when set, answers Lyrics in place of lyrics and lyricsErr.
	lyricsFor func(wrapper.LyricsRequestOptions) (string, error)
	// webPlaybackErr fails WebPlayback, which stops an AAC-LC attempt right
	// after it claimed its output path and fetched the cover and lyrics.
	webPlaybackErr error
	// prepareKeys, when set, answers PrepareKeys.
	prepareKeys func(ctx context.Context, songID string, keys []string) error
}

func (f fakeDownloaderWrapper) Status(context.Context) (wrapper.Status, error) {
	return f.status, nil
}

func (f fakeDownloaderWrapper) M3U8(context.Context, string) (string, error) {
	return "", nil
}

func (f fakeDownloaderWrapper) Lyrics(_ context.Context, _ string, opts wrapper.LyricsRequestOptions) (string, error) {
	if f.lyricsFor != nil {
		return f.lyricsFor(opts)
	}
	return f.lyrics, f.lyricsErr
}

func (f fakeDownloaderWrapper) WebPlayback(context.Context, string) (string, error) {
	return "", f.webPlaybackErr
}

func (f fakeDownloaderWrapper) NewDecryptSession(context.Context, string) (wrapper.DecryptSession, error) {
	return identityDecryptSession{}, nil
}

func (f fakeDownloaderWrapper) PrepareKeys(ctx context.Context, songID string, keys []string) error {
	if f.prepareKeys != nil {
		return f.prepareKeys(ctx, songID, keys)
	}
	return nil
}

// identityDecryptSession returns each sample unchanged, standing in for a
// wrapper that "decrypts" test fixtures that were never encrypted.
type identityDecryptSession struct{}

func (identityDecryptSession) DecryptFragment(_ string, samples [][]byte) ([][]byte, error) {
	return samples, nil
}

func (identityDecryptSession) Close() error { return nil }

func (f fakeDownloaderWrapper) License(context.Context, string, string, string) (string, error) {
	return "", nil
}

type fakeDownloaderCatalog struct {
	song             applemusic.Song
	songErr          error
	album            applemusic.Collection
	playlist         applemusic.Collection
	artistAlbums     applemusic.ArtistAlbums
	station          applemusic.Collection
	stationErr       error
	stationToken     *string
	motionArtwork    applemusic.MotionArtwork
	motionArtworkErr error
}

func (f fakeDownloaderCatalog) Song(context.Context, string, string) (applemusic.Song, error) {
	return f.song, f.songErr
}

func (f fakeDownloaderCatalog) SongMetadata(context.Context, string, string) (applemusic.Song, error) {
	return f.song, f.songErr
}

func (f fakeDownloaderCatalog) Album(_ context.Context, _, id string) (applemusic.Collection, error) {
	if f.album.ID == id {
		return f.album, nil
	}
	for _, album := range f.artistAlbums.Albums {
		if album.ID == id {
			return album, nil
		}
	}
	return applemusic.Collection{}, nil
}

func (f fakeDownloaderCatalog) Playlist(context.Context, string, string, string) (applemusic.Collection, error) {
	return f.playlist, nil
}

func (f fakeDownloaderCatalog) StationTracks(_ context.Context, _, _, token string) (applemusic.Collection, error) {
	if f.stationToken != nil {
		*f.stationToken = token
	}
	return f.station, f.stationErr
}

func (f fakeDownloaderCatalog) ArtistAlbums(context.Context, string, string) (applemusic.ArtistAlbums, error) {
	return f.artistAlbums, nil
}

func (f fakeDownloaderCatalog) Artist(context.Context, string, string) (applemusic.Artist, error) {
	return applemusic.Artist{}, nil
}

func (f fakeDownloaderCatalog) FetchCover(context.Context, []string, string, string) ([]byte, error) {
	return nil, nil
}

func (f fakeDownloaderCatalog) MotionArtworkViaWebToken(context.Context, string, string) (applemusic.MotionArtwork, error) {
	return f.motionArtwork, f.motionArtworkErr
}

type recordingReporter struct {
	mu            sync.Mutex
	events        []domain.Event
	items         []domain.JobItem
	existing      []domain.JobItem
	added         []domain.JobItem
	removed       []string
	motionArtwork []motionArtworkWrite
	// missingJobs makes SetJobMotionArtwork report that no row matched, which
	// is what the store does once the job has been deleted.
	missingJobs bool
}

type motionArtworkWrite struct {
	JobID string
	Art   domain.MotionArtwork
}

type metadataCountingCatalog struct {
	fakeDownloaderCatalog

	mu            sync.Mutex
	songs         map[string]applemusic.Song
	metadataSongs map[string]applemusic.Song
	albums        map[string]applemusic.Collection
	songCalls     map[string]int
	metadataCalls map[string]int
	albumCalls    map[string]int
	albumWait     <-chan struct{}
	albumStarted  chan<- struct{}
}

func (c *metadataCountingCatalog) Song(ctx context.Context, _, id string) (applemusic.Song, error) {
	c.mu.Lock()
	c.songCalls[id]++
	song := c.songs[id]
	c.mu.Unlock()
	return song, ctx.Err()
}

func (c *metadataCountingCatalog) SongMetadata(ctx context.Context, _, id string) (applemusic.Song, error) {
	c.mu.Lock()
	c.metadataCalls[id]++
	song := c.metadataSongs[id]
	c.mu.Unlock()
	return song, ctx.Err()
}

func (c *metadataCountingCatalog) Album(ctx context.Context, _, id string) (applemusic.Collection, error) {
	c.mu.Lock()
	c.albumCalls[id]++
	album := c.albums[id]
	c.mu.Unlock()
	if c.albumStarted != nil {
		select {
		case c.albumStarted <- struct{}{}:
		default:
		}
	}
	if c.albumWait != nil {
		select {
		case <-ctx.Done():
			return applemusic.Collection{}, ctx.Err()
		case <-c.albumWait:
		}
	}
	return album, nil
}

func (c *metadataCountingCatalog) counts() (map[string]int, map[string]int, map[string]int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	songs := make(map[string]int, len(c.songCalls))
	for id, count := range c.songCalls {
		songs[id] = count
	}
	metadata := make(map[string]int, len(c.metadataCalls))
	for id, count := range c.metadataCalls {
		metadata[id] = count
	}
	albums := make(map[string]int, len(c.albumCalls))
	for id, count := range c.albumCalls {
		albums[id] = count
	}
	return songs, metadata, albums
}

func (*recordingReporter) SetJob(_ context.Context, job *domain.Job) error {
	job.UpdatedAt = time.Now().UTC()
	return nil
}

// missingJobs makes SetJobMotionArtwork report "no such row", standing in for a
// job deleted while the detached lookup was in flight.
func (r *recordingReporter) SetJobMotionArtwork(_ context.Context, jobID string, art domain.MotionArtwork) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.motionArtwork = append(r.motionArtwork, motionArtworkWrite{JobID: jobID, Art: art})
	return !r.missingJobs, nil
}

func (r *recordingReporter) AddItem(_ context.Context, item *domain.JobItem) error {
	now := time.Now().UTC()
	item.CreatedAt = now
	item.UpdatedAt = now
	r.mu.Lock()
	defer r.mu.Unlock()
	r.added = append(r.added, *item)
	return nil
}

func (r *recordingReporter) RemoveItem(_ context.Context, itemID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removed = append(r.removed, itemID)
	return nil
}

func (r *recordingReporter) ListItems(context.Context, string) ([]domain.JobItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.JobItem(nil), r.existing...), nil
}

func (r *recordingReporter) UpdateItem(_ context.Context, item *domain.JobItem) error {
	item.UpdatedAt = time.Now().UTC()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, *item)
	return nil
}

func (r *recordingReporter) Event(_ context.Context, ev domain.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
	return nil
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
