package media

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"amdl/internal/applemusic"
	"amdl/internal/config"
	"amdl/internal/domain"
	"amdl/internal/wrapper"
)

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

func (f fakeDownloaderWrapper) Decrypt(context.Context, string, []wrapper.DecryptSample, func(int, int)) ([][]byte, error) {
	return nil, nil
}

func (f fakeDownloaderWrapper) License(context.Context, string, string, string) (string, error) {
	return "", nil
}

type fakeDownloaderCatalog struct {
	song         applemusic.Song
	songErr      error
	album        applemusic.Collection
	artistAlbums applemusic.ArtistAlbums
}

func (f fakeDownloaderCatalog) Song(context.Context, string, string) (applemusic.Song, error) {
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

func (f fakeDownloaderCatalog) Playlist(context.Context, string, string) (applemusic.Collection, error) {
	return applemusic.Collection{}, nil
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

type recordingReporter struct {
	events   []domain.Event
	items    []domain.JobItem
	existing []domain.JobItem
	added    []domain.JobItem
	removed  []string
}

func (*recordingReporter) SetJob(context.Context, domain.Job) error { return nil }
func (r *recordingReporter) AddItem(_ context.Context, item domain.JobItem) error {
	r.added = append(r.added, item)
	return nil
}
func (r *recordingReporter) RemoveItem(_ context.Context, itemID string) error {
	r.removed = append(r.removed, itemID)
	return nil
}
func (r *recordingReporter) ListItems(context.Context, string) ([]domain.JobItem, error) {
	return r.existing, nil
}
func (r *recordingReporter) UpdateItem(_ context.Context, item domain.JobItem) error {
	r.items = append(r.items, item)
	return nil
}
func (r *recordingReporter) Event(_ context.Context, ev domain.Event) error {
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

func TestResolveCollectionArtistFlattensAlbumTracksAndDedupesSongs(t *testing.T) {
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
	if got, want := trackIDsForTest(resolved.Tracks), []string{"song-1", "song-2", "song-3"}; !equalStrings(got, want) {
		t.Fatalf("tracks = %#v, want %#v", got, want)
	}
	if resolved.Name != "Artist" {
		t.Fatalf("collection name = %q, want Artist", resolved.Name)
	}
	if resolved.ArtworkURL != "https://example.com/artist/{w}x{h}bb.jpg" {
		t.Fatalf("artwork url = %q, want artist artwork template", resolved.ArtworkURL)
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
	var encryptedMediaHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/master.m3u8":
			_, _ = w.Write([]byte("#EXTM3U\n" +
				"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"audio-alac-stereo-96000-24\",NAME=\"Lossless\",BIT-DEPTH=24,SAMPLE-RATE=96000\n" +
				"#EXT-X-STREAM-INF:BANDWIDTH=3000000,AVERAGE-BANDWIDTH=2500000,AUDIO=\"audio-alac-stereo-96000-24\",CODECS=\"alac\"\n" +
				"media.m3u8\n"))
		case "/media.m3u8":
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

	downloader := &Downloader{
		cfg:  config.Default(),
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
	if encryptedMediaHits.Load() != 0 {
		t.Fatalf("selectEnhancedMedia downloaded encrypted media %d time(s), want 0", encryptedMediaHits.Load())
	}
	if len(selected.raw) != 0 {
		t.Fatalf("selected.raw length = %d, want 0 before download step", len(selected.raw))
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

	selected, err = downloader.downloadSelectedEnhancedMedia(context.Background(), selected, "alac", func(domain.ItemStatus, float64, string) {})
	if err != nil {
		t.Fatal(err)
	}
	if encryptedMediaHits.Load() != 1 {
		t.Fatalf("downloadSelectedEnhancedMedia downloaded encrypted media %d time(s), want 1", encryptedMediaHits.Load())
	}
	if string(selected.raw) != "encrypted media bytes" {
		t.Fatalf("selected.raw = %q, want encrypted media bytes", string(selected.raw))
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
		{ID: "song-1", Name: "One"},
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
