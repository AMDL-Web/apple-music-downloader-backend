package media

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"amdl/internal/applemusic"
	"amdl/internal/config"
	"amdl/internal/domain"
	"amdl/internal/limits"
	"amdl/internal/wrapper"
)

func TestRunTrackTasksJoinsStartedTasksAndHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var calls atomic.Int32
	done := make(chan error, 1)

	go func() {
		done <- runTrackTasks(ctx, 3, []bool{false, true, false}, func(trackCtx context.Context, index int) error {
			if priority, ok := limits.SubpriorityFromContext(trackCtx); !ok || priority != int64(index) {
				return errors.New("track context does not carry its index as subpriority")
			}
			calls.Add(1)
			started <- struct{}{}
			// Deliberately ignore ctx to prove the scheduler joins work that it
			// has already launched instead of returning early on cancellation.
			<-release
			return nil
		})
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first track task did not start")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("second unfinished track task did not start")
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("scheduler returned before started task exited: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("started tasks after cancellation = %d, want 2 unfinished tracks", got)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("scheduler error = %v, want nil after all tasks were launched", err)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not return after started task exited")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("total started tasks = %d, want 2", got)
	}

	preCanceled, preCancel := context.WithCancel(context.Background())
	preCancel()
	if err := runTrackTasks(preCanceled, 1, []bool{false}, func(context.Context, int) error {
		t.Fatal("task started after cancellation")
		return nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled scheduler error = %v, want context.Canceled", err)
	}
}

type fakeDownloaderWrapper struct {
	status    wrapper.Status
	lyrics    string
	lyricsErr error
}

func (f fakeDownloaderWrapper) Status(context.Context) (wrapper.Status, error) {
	return f.status, nil
}

func (f fakeDownloaderWrapper) M3U8(context.Context, string) (string, error) {
	return "", nil
}

func (f fakeDownloaderWrapper) Lyrics(context.Context, string, wrapper.LyricsRequestOptions) (string, error) {
	return f.lyrics, f.lyricsErr
}

func (f fakeDownloaderWrapper) WebPlayback(context.Context, string) (string, error) {
	return "", nil
}

func (f fakeDownloaderWrapper) NewDecryptSession(context.Context, string) (wrapper.DecryptSession, error) {
	return identityDecryptSession{}, nil
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
	song              applemusic.Song
	songErr           error
	album             applemusic.Collection
	artistAlbums      applemusic.ArtistAlbums
	station           applemusic.Collection
	stationErr        error
	stationToken      *string
	webTokenHLS       string
	webTokenErr       error
	webTokenCallCount *int
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
	return applemusic.Collection{}, nil
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

func (f fakeDownloaderCatalog) EnhancedHLSViaWebToken(context.Context, string, string) (string, error) {
	if f.webTokenCallCount != nil {
		*f.webTokenCallCount++
	}
	return f.webTokenHLS, f.webTokenErr
}

func TestHandleExistingOutputHonorsForceOverwriteConfig(t *testing.T) {
	newExisting := func(t *testing.T) string {
		t.Helper()
		outPath := filepath.Join(t.TempDir(), "track.m4a")
		if err := os.WriteFile(outPath, []byte("existing"), 0o644); err != nil {
			t.Fatal(err)
		}
		return outPath
	}
	cfg := config.Default()
	cfg.Download.TempDir = t.TempDir()

	t.Run("default skips existing file", func(t *testing.T) {
		d := &Downloader{cfg: cfg}
		reporter := &recordingReporter{}
		outPath := newExisting(t)
		item := domain.JobItem{ID: "item-1", JobID: "job-1"}
		skip, err := d.handleExistingOutput(context.Background(), reporter, domain.Job{ID: "job-1"}, &item, outPath)
		if err != nil || !skip {
			t.Fatalf("skip = %v, err = %v, want skip without error", skip, err)
		}
		if item.Status != domain.ItemSkipped {
			t.Fatalf("item status = %q, want skipped", item.Status)
		}
		if _, statErr := os.Stat(outPath); statErr != nil {
			t.Fatalf("existing file was touched: %v", statErr)
		}
	})

	t.Run("global force_overwrite redownloads", func(t *testing.T) {
		forcedCfg := cfg
		forcedCfg.Download.ForceOverwrite = true
		d := &Downloader{cfg: forcedCfg}
		reporter := &recordingReporter{}
		outPath := newExisting(t)
		item := domain.JobItem{ID: "item-1", JobID: "job-1"}
		skip, err := d.handleExistingOutput(context.Background(), reporter, domain.Job{ID: "job-1"}, &item, outPath)
		if err != nil || skip {
			t.Fatalf("skip = %v, err = %v, want overwrite without error", skip, err)
		}
		if _, statErr := os.Stat(outPath); !os.IsNotExist(statErr) {
			t.Fatalf("stale output was not removed: %v", statErr)
		}
	})

	t.Run("legacy job force still overwrites", func(t *testing.T) {
		d := &Downloader{cfg: cfg}
		reporter := &recordingReporter{}
		outPath := newExisting(t)
		item := domain.JobItem{ID: "item-1", JobID: "job-1"}
		skip, err := d.handleExistingOutput(context.Background(), reporter, domain.Job{ID: "job-1", Force: true}, &item, outPath)
		if err != nil || skip {
			t.Fatalf("skip = %v, err = %v, want overwrite without error", skip, err)
		}
		if _, statErr := os.Stat(outPath); !os.IsNotExist(statErr) {
			t.Fatalf("stale output was not removed: %v", statErr)
		}
	})
}

type recordingReporter struct {
	mu       sync.Mutex
	events   []domain.Event
	items    []domain.JobItem
	existing []domain.JobItem
	added    []domain.JobItem
	removed  []string
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

func (*recordingReporter) SetJob(context.Context, domain.Job) error { return nil }
func (r *recordingReporter) AddItem(_ context.Context, item domain.JobItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.added = append(r.added, item)
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
func (r *recordingReporter) UpdateItem(_ context.Context, item domain.JobItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, item)
	return nil
}
func (r *recordingReporter) Event(_ context.Context, ev domain.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
	return nil
}

func TestValidateRequestAcceptsArtistURL(t *testing.T) {
	downloader := &Downloader{
		cfg:     config.Default(),
		wrapper: fakeDownloaderWrapper{status: wrapper.Status{Ready: true, Status: true, Regions: []string{"cn"}}},
	}
	result, err := downloader.ValidateRequest(context.Background(), "https://music.apple.com/cn/artist/example/1495777901")
	if err != nil {
		t.Fatal(err)
	}
	if result.Type != string(applemusic.TypeArtist) || result.Storefront != "cn" {
		t.Fatalf("validation = %+v, want artist/cn", result)
	}
}

func TestResolveCollectionArtistKeepsSharedSongInEveryAlbum(t *testing.T) {
	downloader := &Downloader{
		cfg: config.Default(),
		catalog: fakeDownloaderCatalog{artistAlbums: applemusic.ArtistAlbums{
			Artist: applemusic.Artist{ID: "artist-1", Name: "Artist", ArtworkURL: "https://example.com/artist/{w}x{h}bb.jpg"},
			Albums: []applemusic.Collection{
				{ID: "album-1", Name: "First", Artist: "Artist", Tracks: []applemusic.Song{
					{ID: "song-1", Name: "One", AlbumName: "First", AlbumArtist: "Artist"},
					{ID: "song-2", Name: "Two", AlbumName: "First", AlbumArtist: "Artist"},
				}},
				{ID: "album-2", Name: "Second", Artist: "Artist", Tracks: []applemusic.Song{
					{ID: "song-2", Name: "Two Again", AlbumName: "Second", AlbumArtist: "Artist"},
					{ID: "song-3", Name: "Three", AlbumName: "Second", AlbumArtist: "Artist"},
				}},
			},
		}},
	}

	resolved, err := downloader.resolveCollection(context.Background(), applemusic.ParsedURL{
		Storefront: "cn", Type: applemusic.TypeArtist, ID: "artist-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := trackIDsForTest(resolved.Tracks), []string{"song-1", "song-2", "song-2", "song-3"}; !equalStrings(got, want) {
		t.Fatalf("tracks = %#v, want %#v", got, want)
	}
	if resolved.Tracks[1].AlbumName != "First" || resolved.Tracks[2].AlbumName != "Second" {
		t.Fatalf("shared song album contexts = %q/%q, want First/Second", resolved.Tracks[1].AlbumName, resolved.Tracks[2].AlbumName)
	}
	if resolved.Name != "Artist" {
		t.Fatalf("collection name = %q, want Artist", resolved.Name)
	}
	if resolved.ArtworkURL != "https://example.com/artist/{w}x{h}bb.jpg" {
		t.Fatalf("artwork url = %q, want artist artwork template", resolved.ArtworkURL)
	}
}

func TestResolveCollectionStationPassesTokenAndTracks(t *testing.T) {
	cfg := config.Default()
	cfg.Catalog.MediaUserToken = "media-token-xyz"
	downloader := &Downloader{
		cfg: cfg,
		catalog: fakeDownloaderCatalog{station: applemusic.Collection{
			ID: "ra.1", Type: applemusic.TypeStation, Name: "My Station", ArtworkURL: "station-art",
			Tracks: []applemusic.Song{{ID: "song-1", Name: "One"}, {ID: "song-2", Name: "Two"}},
		}},
	}

	resolved, err := downloader.resolveCollection(context.Background(), applemusic.ParsedURL{
		Storefront: "us", Type: applemusic.TypeStation, ID: "ra.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := trackIDsForTest(resolved.Tracks), []string{"song-1", "song-2"}; !equalStrings(got, want) {
		t.Fatalf("tracks = %#v, want %#v", got, want)
	}
	if resolved.Name != "My Station" || resolved.ID != "ra.1" || resolved.ArtworkURL != "station-art" {
		t.Fatalf("resolved = %+v", resolved)
	}
}

func TestResolveCollectionStationUsesEffectiveConfigToken(t *testing.T) {
	var gotToken string
	cfg := config.Default()
	cfg.Catalog.MediaUserToken = " configured-token "
	downloader := &Downloader{
		cfg: cfg,
		catalog: fakeDownloaderCatalog{
			station:      applemusic.Collection{ID: "ra.1", Type: applemusic.TypeStation, Name: "My Station"},
			stationToken: &gotToken,
		},
	}

	if _, err := downloader.resolveCollection(context.Background(), applemusic.ParsedURL{
		Storefront: "us", Type: applemusic.TypeStation, ID: "ra.1",
	}); err != nil {
		t.Fatal(err)
	}
	if gotToken != "configured-token" {
		t.Fatalf("token = %q, want trimmed configured token", gotToken)
	}

	// ProcessJob applies the request override before resolution; model that
	// effective snapshot directly here and verify it follows the same path.
	requestToken := " request-token "
	downloader.cfg = (&config.DownloadOverrides{MediaUserToken: &requestToken}).Apply(cfg)
	if _, err := downloader.resolveCollection(context.Background(), applemusic.ParsedURL{
		Storefront: "us", Type: applemusic.TypeStation, ID: "ra.1",
	}); err != nil {
		t.Fatal(err)
	}
	if gotToken != "request-token" {
		t.Fatalf("token = %q, want job override to take precedence", gotToken)
	}
}

func TestResolveCollectionStationPropagatesError(t *testing.T) {
	downloader := &Downloader{
		cfg:     config.Default(),
		catalog: fakeDownloaderCatalog{stationErr: errors.New("live radio stations are not downloadable")},
	}
	if _, err := downloader.resolveCollection(context.Background(), applemusic.ParsedURL{
		Storefront: "us", Type: applemusic.TypeStation, ID: "ra.live",
	}); err == nil {
		t.Fatal("expected station resolve error to propagate")
	}
}

func TestResolveCollectionPropagatesArtwork(t *testing.T) {
	tests := []struct {
		name    string
		catalog fakeDownloaderCatalog
		parsed  applemusic.ParsedURL
		want    string
	}{
		{
			name: "song uses its own artwork",
			catalog: fakeDownloaderCatalog{song: applemusic.Song{
				ID: "song-1", ArtworkURL: "https://example.com/song/{w}x{h}bb.jpg", AlbumArtworkURL: "https://example.com/album/{w}x{h}bb.jpg",
			}},
			parsed: applemusic.ParsedURL{Storefront: "cn", Type: applemusic.TypeSong, ID: "song-1"},
			want:   "https://example.com/song/{w}x{h}bb.jpg",
		},
		{
			name: "song falls back to album artwork",
			catalog: fakeDownloaderCatalog{song: applemusic.Song{
				ID: "song-1", AlbumArtworkURL: "https://example.com/album/{w}x{h}bb.jpg",
			}},
			parsed: applemusic.ParsedURL{Storefront: "cn", Type: applemusic.TypeSong, ID: "song-1"},
			want:   "https://example.com/album/{w}x{h}bb.jpg",
		},
		{
			name: "album propagates collection artwork",
			catalog: fakeDownloaderCatalog{album: applemusic.Collection{
				ID: "album-1", Name: "First", ArtworkURL: "https://example.com/album/{w}x{h}bb.jpg",
				Tracks: []applemusic.Song{{ID: "song-1"}},
			}},
			parsed: applemusic.ParsedURL{Storefront: "cn", Type: applemusic.TypeAlbum, ID: "album-1"},
			want:   "https://example.com/album/{w}x{h}bb.jpg",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			downloader := &Downloader{cfg: config.Default(), catalog: tt.catalog}
			resolved, err := downloader.resolveCollection(context.Background(), tt.parsed)
			if err != nil {
				t.Fatal(err)
			}
			if resolved.ArtworkURL != tt.want {
				t.Fatalf("artwork url = %q, want %q", resolved.ArtworkURL, tt.want)
			}
		})
	}
}

func TestTrackMetadataResolverReusesResolvedSingleSong(t *testing.T) {
	catalog := &metadataCountingCatalog{
		metadataCalls: make(map[string]int),
		albumCalls:    make(map[string]int),
	}
	downloader := &Downloader{cfg: config.Default(), catalog: catalog}
	initial := applemusic.Song{
		ID: "song-1", Name: "Song", AlbumID: "album-1", AlbumArtist: "Album Artist",
		TrackCount: 10, DiscCount: 2, HasLyrics: true,
	}

	got, err := newTrackMetadataResolver(downloader, "cn").song(context.Background(), initial, applemusic.TypeSong)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != initial.ID || got.Name != initial.Name || got.AlbumID != initial.AlbumID || got.TrackCount != initial.TrackCount || got.DiscCount != initial.DiscCount || got.HasLyrics != initial.HasLyrics {
		t.Fatalf("song = %+v, want resolved value %+v", got, initial)
	}
	_, metadataCalls, albumCalls := catalog.counts()
	if len(metadataCalls) != 0 || len(albumCalls) != 0 {
		t.Fatalf("metadata calls = %v, album calls = %v, want no duplicate reads", metadataCalls, albumCalls)
	}
}

func TestTrackMetadataResolverMergesResolvedAlbumWithoutRefetch(t *testing.T) {
	catalog := &metadataCountingCatalog{
		metadataSongs: map[string]applemusic.Song{
			"song-1": {ID: "song-1", Name: "Fresh Song", AlbumID: "album-1", HasLyrics: false},
		},
		metadataCalls: make(map[string]int),
		albumCalls:    make(map[string]int),
	}
	downloader := &Downloader{cfg: config.Default(), catalog: catalog}
	initial := applemusic.Song{
		ID: "song-1", Name: "Old Song", AlbumID: "album-1", AlbumName: "Album",
		AlbumArtist: "Album Artist", AlbumArtistID: "artist-1", AlbumArtworkURL: "album-art",
		AlbumArtistArtworkURL: "artist-art", AlbumRelease: "2026-01-01", Copyright: "Copyright",
		RecordLabel: "Label", UPC: "123456", TrackCount: 10, DiscCount: 2, HasLyrics: true,
	}

	got, err := newTrackMetadataResolver(downloader, "cn").song(context.Background(), initial, applemusic.TypeAlbum)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Fresh Song" || got.HasLyrics {
		t.Fatalf("refreshed fields = %q/hasLyrics=%v, want Fresh Song/false", got.Name, got.HasLyrics)
	}
	if got.AlbumArtist != "Album Artist" || got.AlbumArtistID != "artist-1" || got.AlbumArtworkURL != "album-art" || got.TrackCount != 10 || got.DiscCount != 2 || got.UPC != "123456" {
		t.Fatalf("album fields were not preserved: %+v", got)
	}
	_, metadataCalls, albumCalls := catalog.counts()
	if metadataCalls["song-1"] != 1 || len(albumCalls) != 0 {
		t.Fatalf("metadata calls = %v, album calls = %v, want one lightweight song read and no album read", metadataCalls, albumCalls)
	}
}

func TestTrackMetadataResolverPreservesArtistAlbumPlacement(t *testing.T) {
	catalog := &metadataCountingCatalog{
		metadataSongs: map[string]applemusic.Song{
			"song-1": {
				ID: "song-1", Name: "Fresh Song", AlbumID: "default-album", AlbumName: "Default Album",
				ArtworkURL: "default-art", TrackNumber: 1, DiscNumber: 1,
			},
		},
		metadataCalls: make(map[string]int),
		albumCalls:    make(map[string]int),
	}
	downloader := &Downloader{cfg: config.Default(), catalog: catalog}
	initial := applemusic.Song{
		ID: "song-1", Name: "Song", AlbumID: "second-album", AlbumName: "Second Album",
		ArtworkURL: "second-art", TrackNumber: 7, DiscNumber: 2,
	}

	got, err := newTrackMetadataResolver(downloader, "cn").song(context.Background(), initial, applemusic.TypeArtist)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Fresh Song" {
		t.Fatalf("song name = %q, want refreshed name Fresh Song", got.Name)
	}
	if got.AlbumID != "second-album" || got.AlbumName != "Second Album" || got.ArtworkURL != "second-art" || got.TrackNumber != 7 || got.DiscNumber != 2 {
		t.Fatalf("artist album placement was not preserved: %+v", got)
	}
}

func TestTrackMetadataResolverCoalescesPlaylistAlbumReads(t *testing.T) {
	albumWait := make(chan struct{})
	albumStarted := make(chan struct{}, 1)
	catalog := &metadataCountingCatalog{
		metadataSongs: map[string]applemusic.Song{
			"song-1": {ID: "song-1", Name: "One", AlbumID: "album-1"},
			"song-2": {ID: "song-2", Name: "Two", AlbumID: "album-1"},
		},
		albums: map[string]applemusic.Collection{
			"album-1": {
				ID: "album-1", Artist: "Album Artist", ArtworkURL: "album-art", ArtistID: "artist-1", ArtistArtworkURL: "artist-art",
				Tracks: []applemusic.Song{
					{ID: "song-1", TrackCount: 2, DiscNumber: 1, DiscCount: 1},
					{ID: "song-2", TrackCount: 2, DiscNumber: 1, DiscCount: 1},
				},
			},
		},
		metadataCalls: make(map[string]int),
		albumCalls:    make(map[string]int),
		albumWait:     albumWait,
		albumStarted:  albumStarted,
	}
	downloader := &Downloader{cfg: config.Default(), catalog: catalog}
	resolver := newTrackMetadataResolver(downloader, "cn")
	results := make(chan applemusic.Song, 2)
	errs := make(chan error, 2)

	go func() {
		song, err := resolver.song(context.Background(), applemusic.Song{ID: "song-1"}, applemusic.TypePlaylist)
		results <- song
		errs <- err
	}()
	<-albumStarted
	go func() {
		song, err := resolver.song(context.Background(), applemusic.Song{ID: "song-2"}, applemusic.TypePlaylist)
		results <- song
		errs <- err
	}()
	close(albumWait)

	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		song := <-results
		if song.AlbumArtist != "Album Artist" || song.AlbumArtworkURL != "album-art" || song.TrackCount != 2 || song.DiscCount != 1 {
			t.Fatalf("playlist song was not enriched: %+v", song)
		}
	}
	_, metadataCalls, albumCalls := catalog.counts()
	if metadataCalls["song-1"] != 1 || metadataCalls["song-2"] != 1 || albumCalls["album-1"] != 1 {
		t.Fatalf("metadata calls = %v, album calls = %v, want one song read each and one shared album read", metadataCalls, albumCalls)
	}
}

func TestTrackMetadataResolverEnrichesStationTracksWithAlbum(t *testing.T) {
	catalog := &metadataCountingCatalog{
		metadataSongs: map[string]applemusic.Song{
			"song-1": {ID: "song-1", Name: "One", AlbumID: "album-1", AlbumArtist: "Album Artist"},
			"song-2": {ID: "song-2", Name: "Two", AlbumID: "album-1", AlbumArtist: "Album Artist"},
		},
		albums: map[string]applemusic.Collection{
			"album-1": {
				ID: "album-1", Artist: "Album Artist", ArtworkURL: "album-art", ArtistID: "artist-1", ArtistArtworkURL: "artist-art",
				Tracks: []applemusic.Song{
					{ID: "song-1", TrackCount: 2, DiscNumber: 2, DiscCount: 2},
					{ID: "song-2", TrackCount: 2, DiscNumber: 1, DiscCount: 2},
				},
			},
		},
		metadataCalls: make(map[string]int),
		albumCalls:    make(map[string]int),
	}
	downloader := &Downloader{cfg: config.Default(), catalog: catalog}
	resolver := newTrackMetadataResolver(downloader, "cn")

	// Station tracks come from the next-tracks feed: their album relationship
	// carries name/artist but no per-track disc totals or album artist id, so
	// they need the same shared album enrichment as playlist tracks.
	for _, id := range []string{"song-1", "song-2"} {
		song, err := resolver.song(context.Background(), applemusic.Song{ID: id}, applemusic.TypeStation)
		if err != nil {
			t.Fatal(err)
		}
		if song.DiscCount != 2 || song.AlbumArtistID != "artist-1" || song.AlbumArtworkURL != "album-art" {
			t.Fatalf("station song %s was not album-enriched: %+v", id, song)
		}
	}
	_, metadataCalls, albumCalls := catalog.counts()
	if metadataCalls["song-1"] != 1 || metadataCalls["song-2"] != 1 || albumCalls["album-1"] != 1 {
		t.Fatalf("metadata calls = %v, album calls = %v, want one song read each and one shared album read", metadataCalls, albumCalls)
	}
}

func TestProcessJobAvoidsDuplicateSingleSongMetadataReads(t *testing.T) {
	catalog := &metadataCountingCatalog{
		songs: map[string]applemusic.Song{
			"song-1": {
				ID: "song-1", Name: "Song", ArtistName: "Artist", AlbumID: "album-1", AlbumName: "Album",
				AlbumArtist: "Album Artist", TrackCount: 1, DiscCount: 1, DurationInMillis: 1000,
			},
		},
		songCalls:     make(map[string]int),
		metadataCalls: make(map[string]int),
		albumCalls:    make(map[string]int),
	}
	cfg := config.Default()
	cfg.Simulate = config.SimulateConfig{Enabled: true, MinSpeedKBps: 1_000_000, MaxSpeedKBps: 1_000_000}
	cfg.Download.DownloadsDir = t.TempDir()
	cfg.Download.MaxAttempts = 1
	cfg.Download.MaxParallelDownloads = 1
	cfg.Download.MaxParallelDecrypts = 1
	cfg.Download.EmbedCover = false
	cfg.Download.EmbedLyrics = false
	cfg.Download.QualityPriority = []string{"alac"}
	downloader := &Downloader{cfg: cfg, catalog: catalog}
	reporter := &recordingReporter{}
	job := domain.Job{
		ID: "job-1", Input: "https://music.apple.com/cn/song/song/song-1",
		CanonicalKey: "song:cn:song-1", Force: true,
	}

	if err := downloader.ProcessJob(context.Background(), job, reporter); err != nil {
		t.Fatal(err)
	}
	songCalls, metadataCalls, albumCalls := catalog.counts()
	if songCalls["song-1"] != 1 || len(metadataCalls) != 0 || len(albumCalls) != 0 {
		t.Fatalf("song calls = %v, metadata calls = %v, album calls = %v, want one complete song read only", songCalls, metadataCalls, albumCalls)
	}
	if len(reporter.items) == 0 || reporter.items[len(reporter.items)-1].Status != domain.ItemCompleted {
		t.Fatalf("job did not complete: %+v", reporter.items)
	}
}

func TestProcessJobDoesNotRefetchResolvedAlbumPerTrack(t *testing.T) {
	tracks := []applemusic.Song{
		{ID: "song-1", Name: "One", AlbumID: "album-1", AlbumName: "Album", AlbumArtist: "Album Artist", TrackCount: 2, DiscCount: 1, DurationInMillis: 1000},
		{ID: "song-2", Name: "Two", AlbumID: "album-1", AlbumName: "Album", AlbumArtist: "Album Artist", TrackCount: 2, DiscCount: 1, DurationInMillis: 1000},
	}
	catalog := &metadataCountingCatalog{
		metadataSongs: map[string]applemusic.Song{
			"song-1": {ID: "song-1", Name: "One", AlbumID: "album-1", DurationInMillis: 1000},
			"song-2": {ID: "song-2", Name: "Two", AlbumID: "album-1", DurationInMillis: 1000},
		},
		albums: map[string]applemusic.Collection{
			"album-1": {ID: "album-1", Name: "Album", Artist: "Album Artist", Tracks: tracks},
		},
		songCalls:     make(map[string]int),
		metadataCalls: make(map[string]int),
		albumCalls:    make(map[string]int),
	}
	cfg := config.Default()
	cfg.Simulate = config.SimulateConfig{Enabled: true, MinSpeedKBps: 1_000_000, MaxSpeedKBps: 1_000_000}
	cfg.Download.DownloadsDir = t.TempDir()
	cfg.Download.MaxAttempts = 1
	cfg.Download.MaxParallelDownloads = 1
	cfg.Download.MaxParallelDecrypts = 1
	cfg.Download.EmbedCover = false
	cfg.Download.EmbedLyrics = false
	cfg.Download.QualityPriority = []string{"alac"}
	downloader := &Downloader{cfg: cfg, catalog: catalog}
	reporter := &recordingReporter{}
	job := domain.Job{
		ID: "job-1", Input: "https://music.apple.com/cn/album/album/album-1",
		CanonicalKey: "album:cn:album-1", Force: true,
	}

	if err := downloader.ProcessJob(context.Background(), job, reporter); err != nil {
		t.Fatal(err)
	}
	songCalls, metadataCalls, albumCalls := catalog.counts()
	if len(songCalls) != 0 || metadataCalls["song-1"] != 1 || metadataCalls["song-2"] != 1 || albumCalls["album-1"] != 1 {
		t.Fatalf("song calls = %v, metadata calls = %v, album calls = %v, want one album resolve and one lightweight read per track", songCalls, metadataCalls, albumCalls)
	}
	if len(reporter.added) != 2 {
		t.Fatalf("added items = %d, want 2", len(reporter.added))
	}
}

func TestProcessTrackItemProgressEventOmitsArtworkURL(t *testing.T) {
	cfg := config.Default()
	cfg.Download.MaxAttempts = 1 // avoid retry backoff delays; the fetch failure is the point of this test
	downloader := &Downloader{
		cfg:     cfg,
		catalog: fakeDownloaderCatalog{songErr: errors.New("boom")},
	}
	reporter := &recordingReporter{}
	item := domain.JobItem{ID: "item-1", ArtworkURL: "https://example.com/track/{w}x{h}bb.jpg"}
	job := domain.Job{ID: "job-1"}

	if err := downloader.processTrack(context.Background(), job, item, applemusic.Song{ID: "song-1"}, "cn", applemusic.TypeAlbum, "Album", "album-1", 1, "", reporter); err == nil {
		t.Fatal("expected error from failing metadata fetch")
	}

	var progressEvents int
	for _, ev := range reporter.events {
		if ev.Type != "item_progress" {
			continue
		}
		progressEvents++
		if strings.Contains(ev.Payload, "artwork_url") {
			t.Fatalf("item_progress payload leaked artwork_url: %s", ev.Payload)
		}
	}
	if progressEvents == 0 {
		t.Fatal("expected at least one item_progress event before the fetch failed")
	}
}

// TestProcessTrackRefreshesHasLyricsFromCatalogSong drives processTrack past a
// successful per-track metadata fetch (simulate mode with an empty
// quality_priority stops it right after the refresh) and verifies the catalog
// data corrects the item's has_lyrics flag, as the OpenAPI contract documents.
func TestProcessTrackRefreshesHasLyricsFromCatalogSong(t *testing.T) {
	cfg := config.Default()
	cfg.Download.MaxAttempts = 1
	cfg.Simulate.Enabled = true
	cfg.Download.QualityPriority = nil
	downloader := &Downloader{
		cfg:     cfg,
		catalog: fakeDownloaderCatalog{song: applemusic.Song{ID: "song-1", Name: "One", HasLyrics: true}},
	}
	reporter := &recordingReporter{}
	item := domain.JobItem{ID: "item-1", JobID: "job-1", AdamID: "song-1"}
	job := domain.Job{ID: "job-1"}

	if err := downloader.processTrack(context.Background(), job, item, applemusic.Song{ID: "song-1"}, "cn", applemusic.TypeAlbum, "Album", "album-1", 1, "", reporter); err == nil {
		t.Fatal("expected error from empty quality_priority")
	}

	var refreshed bool
	for _, updated := range reporter.items {
		if updated.HasLyrics {
			if updated.Title != "One" {
				t.Fatalf("update carrying has_lyrics=true has title %q, want the refreshed catalog title", updated.Title)
			}
			refreshed = true
			break
		}
	}
	if !refreshed {
		t.Fatalf("no UpdateItem call carried has_lyrics=true after the metadata refresh; items = %+v", reporter.items)
	}
}

// TestProcessTrackRecordsLyricsStatus exercises the real (non-simulate)
// lyrics phase for every outcome. A ToolChecker pointing at a nonexistent
// ffmpeg stops processTrack right after the lyrics block, so no network or
// disk activity follows; the last persisted item update must carry the
// durable lyrics_status for that outcome.
func TestProcessTrackRecordsLyricsStatus(t *testing.T) {
	cases := []struct {
		name      string
		embed     bool
		hasLyrics bool
		lyrics    string
		lyricsErr error
		want      domain.LyricsStatus
	}{
		{name: "fetched", embed: true, hasLyrics: true, lyrics: "<tt>line</tt>", want: domain.LyricsFetched},
		{name: "fetch failed", embed: true, hasLyrics: true, lyricsErr: errors.New("region mismatch"), want: domain.LyricsFailed},
		{name: "empty document", embed: true, hasLyrics: true, lyrics: "", want: domain.LyricsFailed},
		{name: "catalog has none", embed: true, hasLyrics: false, want: domain.LyricsNone},
		{name: "disabled in config", embed: false, hasLyrics: true, want: domain.LyricsDisabled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Download.MaxAttempts = 1
			cfg.Download.EmbedCover = false
			cfg.Download.EmbedLyrics = tc.embed
			cfg.Download.SaveLyricsFile = false
			cfg.Download.LyricsFormat = "ttml" // pass the fetched raw through unconverted
			cfg.Tools.FFmpeg = "amdl-test-missing-ffmpeg"
			downloader := &Downloader{
				cfg:     cfg,
				catalog: fakeDownloaderCatalog{song: applemusic.Song{ID: "song-1", Name: "One", HasLyrics: tc.hasLyrics}},
				wrapper: fakeDownloaderWrapper{lyrics: tc.lyrics, lyricsErr: tc.lyricsErr},
				tools:   NewToolChecker(cfg.Tools),
			}
			reporter := &recordingReporter{}
			item := domain.JobItem{ID: "item-1", JobID: "job-1", AdamID: "song-1"}

			if err := downloader.processTrack(context.Background(), domain.Job{ID: "job-1"}, item, applemusic.Song{ID: "song-1"}, "cn", applemusic.TypeAlbum, "Album", "album-1", 1, "", reporter); err == nil {
				t.Fatal("expected the missing-ffmpeg error to stop processing after the lyrics phase")
			}
			if len(reporter.items) == 0 {
				t.Fatal("no item updates recorded")
			}
			last := reporter.items[len(reporter.items)-1]
			if last.LyricsStatus != tc.want {
				t.Fatalf("lyrics_status = %q, want %q (updates: %+v)", last.LyricsStatus, tc.want, reporter.items)
			}
		})
	}
}

func TestSelectEnhancedMediaDoesNotDownloadEncryptedMedia(t *testing.T) {
	var masterHits atomic.Int32
	var mediaPlaylistHits atomic.Int32
	var encryptedMediaHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/master.m3u8":
			masterHits.Add(1)
			_, _ = w.Write([]byte("#EXTM3U\n" +
				"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"audio-alac-stereo-96000-24\",NAME=\"Lossless\",BIT-DEPTH=24,SAMPLE-RATE=96000\n" +
				"#EXT-X-STREAM-INF:BANDWIDTH=3000000,AVERAGE-BANDWIDTH=2500000,AUDIO=\"audio-alac-stereo-96000-24\",CODECS=\"alac\"\n" +
				"media.m3u8\n"))
		case "/media.m3u8":
			mediaPlaylistHits.Add(1)
			_, _ = w.Write([]byte("#EXTM3U\n" +
				"#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"skd://itunes.apple.com/P000000000/s1/e1/c23\"\n" +
				"#EXT-X-MAP:URI=\"encrypted.mp4\"\n"))
		case "/encrypted.mp4":
			encryptedMediaHits.Add(1)
			_, _ = w.Write([]byte("encrypted media bytes"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Download.TempDir = t.TempDir()
	downloader := &Downloader{
		cfg:  cfg,
		http: server.Client(),
	}
	item := &domain.JobItem{ID: "item-1"}
	reporter := &recordingReporter{}
	selected, err := downloader.selectEnhancedMedia(
		context.Background(),
		domain.Job{ID: "job-1"},
		item,
		applemusic.Song{ID: "song-1", EnhancedHLS: server.URL + "/master.m3u8"},
		"alac",
		reporter,
		func(domain.ItemStatus, float64, string) {},
	)
	if err != nil {
		t.Fatal(err)
	}
	if masterHits.Load() != 1 || mediaPlaylistHits.Load() != 1 {
		t.Fatalf("selection playlist requests = master:%d media:%d, want master:1 media:1", masterHits.Load(), mediaPlaylistHits.Load())
	}
	if encryptedMediaHits.Load() != 0 {
		t.Fatalf("selectEnhancedMedia downloaded encrypted media %d time(s), want 0", encryptedMediaHits.Load())
	}
	if selected.rawPath != "" {
		t.Fatalf("selected.rawPath = %q, want empty before download step", selected.rawPath)
	}
	if len(selected.raw) != 0 {
		t.Fatalf("selected.raw has %d bytes before download step, want 0", len(selected.raw))
	}
	if got, want := qualityLabel(selected.info), "24-bit/96 kHz"; got != want {
		t.Fatalf("qualityLabel = %q, want %q", got, want)
	}
	if item.BitDepth != 24 || item.SampleRate != 96000 || item.Bitrate != 2500000 {
		t.Fatalf("item quality = %+v, want bit_depth=24 sample_rate=96000 bitrate=2500000", item)
	}
	if len(reporter.items) == 0 {
		t.Fatal("selectEnhancedMedia did not persist the resolved quality via UpdateItem")
	}
	last := reporter.items[len(reporter.items)-1]
	if last.BitDepth != 24 || last.SampleRate != 96000 || last.Bitrate != 2500000 {
		t.Fatalf("persisted item quality = %+v, want bit_depth=24 sample_rate=96000 bitrate=2500000", last)
	}

	selected, err = downloader.downloadSelectedEnhancedMedia(context.Background(), selected, "alac", "job-1", filepath.Join(cfg.Download.DownloadsDir, "song.m4a"), func(domain.ItemStatus, float64, string) {})
	if err != nil {
		t.Fatal(err)
	}
	if encryptedMediaHits.Load() != 1 {
		t.Fatalf("downloadSelectedEnhancedMedia downloaded encrypted media %d time(s), want 1", encryptedMediaHits.Load())
	}
	if selected.rawPath == "" {
		t.Fatal("downloadSelectedEnhancedMedia did not set rawPath after download step")
	}
	gotRaw, err := os.ReadFile(selected.rawPath)
	if err != nil {
		t.Fatalf("read downloaded media file: %v", err)
	}
	if string(gotRaw) != "encrypted media bytes" {
		t.Fatalf("downloaded media = %q, want encrypted media bytes", string(gotRaw))
	}
	if len(selected.raw) != 0 {
		t.Fatalf("low-memory download retained %d bytes in memory, want 0", len(selected.raw))
	}
}

func TestDownloadSelectedEnhancedMediaHighKeepsOnlyMemoryCopy(t *testing.T) {
	const payload = "encrypted media bytes"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()

	tempDir := t.TempDir()
	cfg := config.Default()
	cfg.Download.MemoryMode = config.MemoryModeHigh
	cfg.Download.TempDir = tempDir
	downloader := &Downloader{cfg: cfg, http: server.Client()}
	selected, err := downloader.downloadSelectedEnhancedMedia(
		context.Background(),
		selectedDownloadMedia{info: selectedMediaInfo{MediaURI: server.URL}},
		"alac",
		"job-high",
		filepath.Join(cfg.Download.DownloadsDir, "song.m4a"),
		func(domain.ItemStatus, float64, string) {},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(selected.raw); got != payload {
		t.Fatalf("selected.raw = %q, want %q", got, payload)
	}
	if selected.rawPath != "" {
		t.Fatalf("selected.rawPath = %q, want no scratch file in high-memory mode", selected.rawPath)
	}
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("high-memory download created temp entries: %v", entries)
	}
}

func TestDownloadSelectedEnhancedMediaHighResumesInterruptedTransfer(t *testing.T) {
	payload := []byte(strings.Repeat("high-memory-encrypted-media", 8192))
	cut := 71 * 1024
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requests.Add(1)
		w.Header().Set("ETag", `"high-v1"`)
		if requestNumber == 1 {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			_, _ = w.Write(payload[:cut])
			return
		}
		if got, want := r.Header.Get("Range"), "bytes="+strconv.Itoa(cut)+"-"; got != want {
			t.Errorf("Range = %q, want %q", got, want)
		}
		if got := r.Header.Get("If-Range"); got != `"high-v1"` {
			t.Errorf("If-Range = %q, want high-memory ETag", got)
		}
		w.Header().Set("Content-Range", "bytes "+strconv.Itoa(cut)+"-"+strconv.Itoa(len(payload)-1)+"/"+strconv.Itoa(len(payload)))
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)-cut))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[cut:])
	}))
	defer server.Close()

	tempDir := t.TempDir()
	cfg := config.Default()
	cfg.Download.MemoryMode = config.MemoryModeHigh
	cfg.Download.TempDir = tempDir
	downloader := &Downloader{cfg: cfg, http: server.Client()}
	selected, err := downloader.downloadSelectedEnhancedMedia(
		context.Background(),
		selectedDownloadMedia{info: selectedMediaInfo{MediaURI: server.URL}},
		"alac",
		"job-high-resume",
		filepath.Join(cfg.Download.DownloadsDir, "song.m4a"),
		func(domain.ItemStatus, float64, string) {},
	)
	if err != nil {
		t.Fatal(err)
	}
	if selected.releaseInFlight == nil {
		t.Fatal("high-memory download did not retain its in-flight permit")
	}
	selected.releaseInFlight()
	if !bytes.Equal(selected.raw, payload) {
		t.Fatalf("selected.raw has %d bytes, want %d", len(selected.raw), len(payload))
	}
	if selected.rawPath != "" {
		t.Fatalf("selected.rawPath = %q, want no scratch file", selected.rawPath)
	}
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("high-memory resume created temp entries: %v", entries)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want initial plus one Range resume", got)
	}
}

func TestSetItemQualityUpdatesItemAndPersists(t *testing.T) {
	downloader := &Downloader{}
	reporter := &recordingReporter{}
	item := domain.JobItem{ID: "item-1"}
	downloader.setItemQuality(context.Background(), reporter, &item, 24, 96000, 3000000)

	if item.BitDepth != 24 || item.SampleRate != 96000 || item.Bitrate != 3000000 {
		t.Fatalf("item quality = %+v, want bit_depth=24 sample_rate=96000 bitrate=3000000", item)
	}
	if len(reporter.items) != 1 {
		t.Fatalf("setItemQuality persisted %d items, want 1", len(reporter.items))
	}
	if got := reporter.items[0]; got.BitDepth != 24 || got.SampleRate != 96000 || got.Bitrate != 3000000 {
		t.Fatalf("persisted item quality = %+v, want bit_depth=24 sample_rate=96000 bitrate=3000000", got)
	}
}

func trackIDsForTest(tracks []applemusic.Song) []string {
	out := make([]string, 0, len(tracks))
	for _, track := range tracks {
		out = append(out, track.ID)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestCleanupFailedOutputRemovesPartFile(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "song.m4a")
	for _, path := range []string{outPath, outPath + partSuffix, filepath.Join(dir, "song.lrc"), filepath.Join(dir, "song.ttml")} {
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	cleanupFailedOutput(outPath)
	for _, path := range []string{outPath, outPath + partSuffix, filepath.Join(dir, "song.lrc"), filepath.Join(dir, "song.ttml")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s still exists after cleanupFailedOutput", path)
		}
	}
}

func TestSyncJobItemsReusesPreviousRowsAndSkipsFinishedTracks(t *testing.T) {
	reporter := &recordingReporter{existing: []domain.JobItem{
		{ID: "item-done", JobID: "job-1", AdamID: "song-1", Kind: "song", Index: 1, Status: domain.ItemCompleted, Progress: 1, Codec: "alac"},
		{ID: "item-failed", JobID: "job-1", AdamID: "song-2", Kind: "song", Index: 2, Status: domain.ItemFailed, Progress: 0.4, Codec: "alac", RetryKind: "download", Attempt: 3, MaxAttempts: 3, Error: "boom"},
		{ID: "item-stale", JobID: "job-1", AdamID: "song-gone", Kind: "song", Index: 3, Status: domain.ItemFailed},
	}}
	tracks := []applemusic.Song{
		{ID: "song-1", Name: "One", HasLyrics: true},
		{ID: "song-2", Name: "Two"},
		{ID: "song-3", Name: "Three"},
	}

	items, finished, err := syncJobItems(context.Background(), domain.Job{ID: "job-1"}, tracks, reporter)
	if err != nil {
		t.Fatal(err)
	}

	if !finished[0] || finished[1] || finished[2] {
		t.Fatalf("finished = %v, want only the completed track flagged", finished)
	}
	if items[0].ID != "item-done" || items[0].Status != domain.ItemCompleted {
		t.Fatalf("completed item = %+v, want reused item-done untouched", items[0])
	}
	// Reused rows take the fresh catalog has_lyrics at resolve time — even
	// finished ones, which never reach the per-track metadata refresh — and
	// the correction is persisted.
	if !items[0].HasLyrics {
		t.Fatalf("completed item = %+v, want has_lyrics refreshed from the resolved track", items[0])
	}
	var persistedDone bool
	for _, updated := range reporter.items {
		if updated.ID == "item-done" && updated.HasLyrics {
			persistedDone = true
		}
	}
	if !persistedDone {
		t.Fatalf("updates = %+v, want the finished item's has_lyrics refresh persisted", reporter.items)
	}
	if items[1].ID != "item-failed" {
		t.Fatalf("failed track item id = %s, want reused item-failed", items[1].ID)
	}
	if items[1].Status != domain.ItemQueued || items[1].Progress != 0 || items[1].Codec != "" || items[1].Error != "" || items[1].Attempt != 0 {
		t.Fatalf("failed item was not reset for retry: %+v", items[1])
	}
	if len(reporter.added) != 1 || reporter.added[0].AdamID != "song-3" || reporter.added[0].Status != domain.ItemQueued {
		t.Fatalf("added items = %+v, want one fresh queued item for song-3", reporter.added)
	}
	if items[2].ID != reporter.added[0].ID {
		t.Fatalf("new track item id = %s, want the freshly added row", items[2].ID)
	}
	if len(reporter.removed) != 1 || reporter.removed[0] != "item-stale" {
		t.Fatalf("removed items = %v, want [item-stale]", reporter.removed)
	}
}

func TestSyncJobItemsFirstRunCreatesAllItems(t *testing.T) {
	reporter := &recordingReporter{}
	tracks := []applemusic.Song{{ID: "song-1", Name: "One", HasLyrics: true}, {ID: "song-2", Name: "Two"}}

	items, finished, err := syncJobItems(context.Background(), domain.Job{ID: "job-1"}, tracks, reporter)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || len(reporter.added) != 2 || len(reporter.removed) != 0 {
		t.Fatalf("items=%d added=%d removed=%d, want 2/2/0", len(items), len(reporter.added), len(reporter.removed))
	}
	for i := range items {
		if finished[i] {
			t.Fatalf("finished[%d] = true on first run", i)
		}
		if items[i].Status != domain.ItemQueued || items[i].Index != i+1 {
			t.Fatalf("item %d = %+v, want queued at index %d", i, items[i], i+1)
		}
		if items[i].HasLyrics != tracks[i].HasLyrics {
			t.Fatalf("item %d has_lyrics = %v, want %v from the resolved track", i, items[i].HasLyrics, tracks[i].HasLyrics)
		}
	}
}
