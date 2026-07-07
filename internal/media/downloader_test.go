package media

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"amdl/internal/applemusic"
	"amdl/internal/config"
	"amdl/internal/domain"
	"amdl/internal/wrapper"
)

type fakeDownloaderWrapper struct {
	status wrapper.Status
}

func (f fakeDownloaderWrapper) Status(context.Context) (wrapper.Status, error) {
	return f.status, nil
}

func (f fakeDownloaderWrapper) M3U8(context.Context, string) (string, error) {
	return "", nil
}

func (f fakeDownloaderWrapper) Lyrics(context.Context, string, wrapper.LyricsRequestOptions) (string, error) {
	return "", nil
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

type noopReporter struct{}

func (noopReporter) SetJob(context.Context, domain.Job) error         { return nil }
func (noopReporter) AddItem(context.Context, domain.JobItem) error    { return nil }
func (noopReporter) UpdateItem(context.Context, domain.JobItem) error { return nil }
func (noopReporter) Event(context.Context, domain.Event) error        { return nil }

type recordingReporter struct {
	events []domain.Event
}

func (*recordingReporter) SetJob(context.Context, domain.Job) error      { return nil }
func (*recordingReporter) AddItem(context.Context, domain.JobItem) error { return nil }
func (*recordingReporter) UpdateItem(context.Context, domain.JobItem) error {
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
	selected, err := downloader.selectEnhancedMedia(
		context.Background(),
		domain.Job{ID: "job-1"},
		&domain.JobItem{ID: "item-1"},
		applemusic.Song{ID: "song-1", EnhancedHLS: server.URL + "/master.m3u8"},
		"alac",
		noopReporter{},
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
