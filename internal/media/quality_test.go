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
	"amdl/internal/jobs"
	"amdl/internal/wrapper"
)

const qualityTestMediaPlaylist = `#EXTM3U
#EXT-X-MAP:URI="media.mp4"
`

func writeQualityTestPlaylist(w http.ResponseWriter, r *http.Request, master string) {
	if r.URL.Path == "/audio.m3u8" {
		_, _ = w.Write([]byte(qualityTestMediaPlaylist))
		return
	}
	_, _ = w.Write([]byte(master))
}

func TestQualityQueryUsesSharedVariantSelectionWithoutMediaPlaylist(t *testing.T) {
	const master = `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio-stereo-256",NAME="AAC",URI="aac/prog.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=281000,AVERAGE-BANDWIDTH=256000,CODECS="mp4a.40.2",AUDIO="audio-stereo-256"
aac/media.m3u8
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio-alac-stereo-96000-24",NAME="ALAC",URI="alac/prog.m3u8",BIT-DEPTH=24,SAMPLE-RATE=96000
#EXT-X-STREAM-INF:BANDWIDTH=4600000,AVERAGE-BANDWIDTH=3900000,CODECS="alac",AUDIO="audio-alac-stereo-96000-24"
alac/media.m3u8
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio-alac-stereo-192000-24",NAME="ALAC Hi-Res",URI="alac-hires/prog.m3u8",BIT-DEPTH=24,SAMPLE-RATE=192000
#EXT-X-STREAM-INF:BANDWIDTH=9200000,AVERAGE-BANDWIDTH=7800000,CODECS="alac",AUDIO="audio-alac-stereo-192000-24"
alac-hires/media.m3u8
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio-atmos-2448",NAME="Atmos",URI="atmos/prog.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=768000,AVERAGE-BANDWIDTH=640000,CODECS="ec-3",AUDIO="audio-atmos-2448"
atmos/media.m3u8
`
	var masterHits, mediaHits int
	manifest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/master.m3u8" {
			masterHits++
			_, _ = w.Write([]byte(master))
			return
		}
		mediaHits++
		_, _ = w.Write([]byte(qualityTestMediaPlaylist))
	}))
	defer manifest.Close()

	cfg := config.Default()
	cfg.Download.MaxAttempts = 1
	cfg.Download.ALACMaxSampleRate = 96000
	service := newTestQualityService(cfg, fakeQualityCatalog{song: applemusic.Song{
		ID: "song-1", Name: "Song", ArtistName: "Artist", AlbumName: "Album", EnhancedHLS: manifest.URL + "/master.m3u8",
	}}, countingQualityWrapper{})
	result, err := service.QueryQuality(context.Background(), QualityRequest{URL: "https://music.apple.com/cn/song/example/song-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Tracks) != 1 || result.Tracks[0].Song.ID != "song-1" {
		t.Fatalf("song result tracks = %#v, want one song-1 track", result.Tracks)
	}
	track := result.Tracks[0]

	want := map[string]QualityOption{
		"aac":  {Available: true, CodecID: "audio-stereo-256", Bitrate: 256000},
		"alac": {Available: true, CodecID: "audio-alac-stereo-96000-24", Bitrate: 3900000, BitDepth: 24, SampleRate: 96000},
		"ec3":  {Available: true, CodecID: "audio-atmos-2448", Bitrate: 640000},
	}
	got := map[string]QualityOption{}
	wantOrder := []string{"aac", "aac-binaural", "aac-downmix", "alac", "ec3", "ac3"}
	for i, quality := range track.Qualities {
		if quality.ID != wantOrder[i] {
			t.Fatalf("quality order[%d] = %q, want %q", i, quality.ID, wantOrder[i])
		}
		got[quality.ID] = quality
	}
	for id, expected := range want {
		actual, ok := got[id]
		if !ok {
			t.Fatalf("quality %q missing from %#v", id, track.Qualities)
		}
		if !actual.Available || actual.CodecID != expected.CodecID || actual.Bitrate != expected.Bitrate || actual.BitDepth != expected.BitDepth || actual.SampleRate != expected.SampleRate {
			t.Fatalf("quality %q = %#v, want fields %#v", id, actual, expected)
		}
	}
	if got["alac"].Description == "" {
		t.Fatalf("ALAC description is empty")
	}
	if masterHits != 1 || mediaHits != 0 {
		t.Fatalf("playlist requests = master:%d media:%d, want master:1 media:0", masterHits, mediaHits)
	}

	selected, err := service.downloader.selectEnhancedStream(context.Background(), "cn", applemusic.Song{
		ID: "song-1", EnhancedHLS: manifest.URL + "/master.m3u8",
	}, "alac")
	if err != nil {
		t.Fatal(err)
	}
	if selected.CodecID != got["alac"].CodecID || selected.BitDepth != got["alac"].BitDepth || selected.SampleRate != got["alac"].SampleRate || selected.Bandwidth != got["alac"].Bitrate {
		t.Fatalf("download selection = %+v, quality variant = %+v", selected, got["alac"])
	}
	if masterHits != 2 || mediaHits != 1 {
		t.Fatalf("combined playlist requests = master:%d media:%d, want master:2 media:1", masterHits, mediaHits)
	}
}

type fakeQualityCatalog struct {
	song applemusic.Song
}

func (f fakeQualityCatalog) Song(context.Context, string, string) (applemusic.Song, error) {
	return f.song, nil
}

func (f fakeQualityCatalog) SongMetadata(context.Context, string, string) (applemusic.Song, error) {
	return f.song, nil
}

func (f fakeQualityCatalog) Album(context.Context, string, string) (applemusic.Collection, error) {
	return applemusic.Collection{}, nil
}

func (f fakeQualityCatalog) Playlist(context.Context, string, string, string) (applemusic.Collection, error) {
	return applemusic.Collection{}, nil
}

func (f fakeQualityCatalog) StationTracks(context.Context, string, string, string) (applemusic.Collection, error) {
	return applemusic.Collection{}, nil
}

func (f fakeQualityCatalog) ArtistAlbums(context.Context, string, string) (applemusic.ArtistAlbums, error) {
	return applemusic.ArtistAlbums{}, nil
}

func (f fakeQualityCatalog) Artist(context.Context, string, string) (applemusic.Artist, error) {
	return applemusic.Artist{}, nil
}

func (f fakeQualityCatalog) FetchCover(context.Context, []string, string, string) ([]byte, error) {
	return nil, nil
}

func (f fakeQualityCatalog) MotionArtworkViaWebToken(context.Context, string, string) (applemusic.MotionArtwork, error) {
	return applemusic.MotionArtwork{}, nil
}

type countingQualityWrapper struct {
	m3u8      string
	err       error
	callCount *int
	status    *wrapper.Status
	statusErr error
}

func (c countingQualityWrapper) Status(context.Context) (wrapper.Status, error) {
	if c.statusErr != nil {
		return wrapper.Status{}, c.statusErr
	}
	if c.status != nil {
		return *c.status, nil
	}
	return wrapper.Status{Ready: true, LoggedIn: true, Regions: []string{"cn"}}, nil
}

func (c countingQualityWrapper) M3U8(context.Context, string) (string, error) {
	if c.callCount != nil {
		*c.callCount++
	}
	return c.m3u8, c.err
}

func (c countingQualityWrapper) Lyrics(context.Context, string, wrapper.LyricsRequestOptions) (string, error) {
	return "", nil
}

func (c countingQualityWrapper) WebPlayback(context.Context, string) (string, error) {
	return "", nil
}

func (countingQualityWrapper) PrepareKeys(context.Context, string, []string) error { return nil }

func (countingQualityWrapper) PrefetchKeys(context.Context, string, []string) error { return nil }

func (c countingQualityWrapper) NewDecryptSession(context.Context, string) (wrapper.DecryptSession, error) {
	return identityDecryptSession{}, nil
}

func (c countingQualityWrapper) License(context.Context, string, string, string) (string, error) {
	return "", nil
}

func TestQualityQueryUsesDownloadStorefrontValidation(t *testing.T) {
	cfg := config.Default()
	cfg.Download.MaxAttempts = 1
	catalog := fakeQualityCatalog{song: applemusic.Song{ID: "song-1", Name: "Song"}}

	tests := []struct {
		name     string
		wrapper  countingQualityWrapper
		wantCode string
	}{
		{name: "decryptor unavailable", wrapper: countingQualityWrapper{statusErr: errors.New("offline")}, wantCode: "decryptor_unavailable"},
		{name: "unsupported storefront", wrapper: countingQualityWrapper{status: &wrapper.Status{Ready: true, LoggedIn: true, Regions: []string{"us"}}}, wantCode: "unsupported_storefront"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := newTestQualityService(cfg, catalog, tt.wrapper)
			_, err := service.QueryQuality(context.Background(), QualityRequest{URL: "https://music.apple.com/cn/song/example/song-1"})
			var requestErr *jobs.RequestError
			if !errors.As(err, &requestErr) || requestErr.Code != tt.wantCode {
				t.Fatalf("error = %v, want request error code %q", err, tt.wantCode)
			}
		})
	}
}

func TestQualityQueryUsesCatalogManifestWithoutWrapperM3U8(t *testing.T) {
	manifest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		writeQualityTestPlaylist(w, r, `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio-stereo-256",NAME="AAC"
#EXT-X-STREAM-INF:BANDWIDTH=281000,AVERAGE-BANDWIDTH=256000,CODECS="mp4a.40.2",AUDIO="audio-stereo-256"
audio.m3u8
`)
	}))
	defer manifest.Close()

	service := newTestQualityService(config.Default(), fakeQualityCatalog{song: applemusic.Song{
		ID: "song-1", Name: "Song", ArtistName: "Artist", AlbumName: "Album", HasLyrics: true, EnhancedHLS: manifest.URL + "/master.m3u8",
	}}, countingQualityWrapper{})

	result, err := service.QueryQuality(context.Background(), QualityRequest{URL: "https://music.apple.com/cn/song/example/song-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Tracks) != 1 || len(result.Tracks[0].Qualities) == 0 || !result.Tracks[0].Qualities[0].Available {
		t.Fatalf("expected catalog manifest qualities, got %#v", result.Tracks)
	}
	if !result.Tracks[0].Song.HasLyrics {
		t.Fatalf("song = %+v, want has_lyrics propagated from catalog", result.Tracks[0].Song)
	}
}

func TestQualityQuerySongWithoutEnhancedHLSOffersNothing(t *testing.T) {
	service := newTestQualityService(config.Default(), fakeQualityCatalog{song: applemusic.Song{
		ID: "song-1", Name: "Song", ArtistName: "Artist", AlbumName: "Album",
	}}, countingQualityWrapper{})

	result, err := service.QueryQuality(context.Background(), QualityRequest{URL: "https://music.apple.com/cn/song/example/song-1"})
	if err != nil {
		t.Fatalf("QueryQuality = %v, want every codec reported as not offered", err)
	}
	if len(result.Tracks) != 1 || len(result.Tracks[0].Qualities) != 6 {
		t.Fatalf("tracks = %#v, want one track with all six codecs", result.Tracks)
	}
	for _, q := range result.Tracks[0].Qualities {
		if q.Available {
			t.Fatalf("quality %s available without an Enhanced HLS master", q.ID)
		}
	}
}

func TestQualityMasterInventoryRetriesSourceAndMasterAsOneUnit(t *testing.T) {
	var masterHits atomic.Int32
	manifest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if masterHits.Add(1) == 1 {
			http.Error(w, "temporary failure", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio-stereo-256",NAME="AAC"
#EXT-X-STREAM-INF:AVERAGE-BANDWIDTH=256000,AUDIO="audio-stereo-256"
audio.m3u8
`))
	}))
	defer manifest.Close()

	cfg := config.Default()
	cfg.Download.MaxAttempts = 2
	var wrapperCalls int
	service := newTestQualityService(cfg, fakeQualityCatalog{song: applemusic.Song{
		ID: "song-1", Name: "Song", ArtistName: "Artist", AlbumName: "Album", HiResLossless: true,
	}}, countingQualityWrapper{m3u8: manifest.URL + "/master.m3u8", callCount: &wrapperCalls})

	result, err := service.QueryQuality(context.Background(), QualityRequest{URL: "https://music.apple.com/cn/song/example/song-1"})
	if err != nil {
		t.Fatal(err)
	}
	if wrapperCalls != 2 || masterHits.Load() != 2 {
		t.Fatalf("retry calls = wrapper:%d master:%d, want wrapper:2 master:2", wrapperCalls, masterHits.Load())
	}
	if len(result.Tracks) != 1 || len(result.Tracks[0].Qualities) == 0 || !result.Tracks[0].Qualities[0].Available {
		t.Fatalf("qualities after retry = %#v", result.Tracks)
	}
}

// hiResManifests serves the two masters of a hi-res-lossless track the
// way Apple did for song 1635388718: the web player's enhancedHls offers ALAC
// only at 48 kHz, while the device manifest also lists 88.2 kHz.
func hiResManifests(t *testing.T) (webMaster, deviceMaster string) {
	t.Helper()
	const alac48 = `#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio-alac-stereo-48000-24",NAME="ALAC",BIT-DEPTH=24,SAMPLE-RATE=48000
#EXT-X-STREAM-INF:BANDWIDTH=2400000,AVERAGE-BANDWIDTH=2000000,CODECS="alac",AUDIO="audio-alac-stereo-48000-24"
alac-48/media.m3u8
`
	const alac88 = `#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio-alac-stereo-88200-24",NAME="ALAC",BIT-DEPTH=24,SAMPLE-RATE=88200
#EXT-X-STREAM-INF:BANDWIDTH=4300000,AVERAGE-BANDWIDTH=3600000,CODECS="alac",AUDIO="audio-alac-stereo-88200-24"
alac-88/media.m3u8
`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/web.m3u8":
			_, _ = w.Write([]byte("#EXTM3U\n" + alac48))
		case "/device.m3u8":
			_, _ = w.Write([]byte("#EXTM3U\n" + alac48 + alac88))
		case "/alac-48/media.m3u8", "/alac-88/media.m3u8":
			_, _ = w.Write([]byte("#EXTM3U\n" +
				"#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"skd://itunes.apple.com/P000000000/s1/e1/c23\"\n" +
				"#EXT-X-MAP:URI=\"encrypted.mp4\"\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL + "/web.m3u8", server.URL + "/device.m3u8"
}

func TestQualityQueryHiResTrackReadsDeviceManifest(t *testing.T) {
	webMaster, deviceMaster := hiResManifests(t)
	tests := []struct {
		name          string
		hiRes         bool
		maxRate       int // alac_max_sample_rate; 0 keeps the default
		wrapper       countingQualityWrapper
		wantRate      int
		wantM3U8Calls int
	}{
		{name: "hi-res track takes the device manifest", hiRes: true, wrapper: countingQualityWrapper{m3u8: deviceMaster}, wantRate: 88200, wantM3U8Calls: 1},
		{name: "wrapper error keeps the web manifest", hiRes: true, wrapper: countingQualityWrapper{err: errors.New("wrapper m3u8: no session")}, wantRate: 48000, wantM3U8Calls: 1},
		{name: "empty device manifest keeps the web manifest", hiRes: true, wrapper: countingQualityWrapper{}, wantRate: 48000, wantM3U8Calls: 1},
		{name: "other tracks never ask the wrapper", hiRes: false, wrapper: countingQualityWrapper{m3u8: deviceMaster}, wantRate: 48000, wantM3U8Calls: 0},
		{name: "a 48 kHz cap never asks the wrapper", hiRes: true, maxRate: 48000, wrapper: countingQualityWrapper{m3u8: deviceMaster}, wantRate: 48000, wantM3U8Calls: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Download.MaxAttempts = 1
			if tt.maxRate != 0 {
				cfg.Download.ALACMaxSampleRate = tt.maxRate
			}
			var calls int
			tt.wrapper.callCount = &calls
			service := newTestQualityService(cfg, fakeQualityCatalog{song: applemusic.Song{
				ID: "song-1", Name: "TV", EnhancedHLS: webMaster, HiResLossless: tt.hiRes,
			}}, tt.wrapper)

			result, err := service.QueryQuality(context.Background(), QualityRequest{URL: "https://music.apple.com/cn/song/example/song-1"})
			if err != nil {
				t.Fatal(err)
			}
			if calls != tt.wantM3U8Calls {
				t.Fatalf("wrapper.M3U8 calls = %d, want %d", calls, tt.wantM3U8Calls)
			}
			var alac QualityOption
			for _, option := range result.Tracks[0].Qualities {
				if option.ID == "alac" {
					alac = option
				}
			}
			if !alac.Available || alac.SampleRate != tt.wantRate {
				t.Fatalf("alac = %+v, want sample rate %d", alac, tt.wantRate)
			}
		})
	}
}

func TestSelectEnhancedStreamHiResDownloadsFromDeviceManifest(t *testing.T) {
	webMaster, deviceMaster := hiResManifests(t)
	var calls int
	d := newTestDownloader(&Downloader{
		cfg: config.Default(), wrapper: countingQualityWrapper{m3u8: deviceMaster, callCount: &calls}, http: newHTTPClient(),
	})
	info, err := d.selectEnhancedStream(context.Background(), "cn", applemusic.Song{ID: "song-1", EnhancedHLS: webMaster, HiResLossless: true}, "alac")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || info.SampleRate != 88200 || info.CodecID != "audio-alac-stereo-88200-24" || !strings.HasSuffix(info.MediaURI, "/alac-88/encrypted.mp4") {
		t.Fatalf("selected %+v after %d wrapper.M3U8 calls, want the 88.2 kHz device rendition", info, calls)
	}
}

func TestResolveEnhancedHLSHiResDoesNotFallBackWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := newTestDownloader(&Downloader{cfg: config.Default(), wrapper: countingQualityWrapper{err: ctx.Err()}})
	master, _, err := d.resolveEnhancedHLS(ctx, "cn", applemusic.Song{ID: "song-1", EnhancedHLS: "https://web.invalid/master.m3u8", HiResLossless: true}, "alac")
	if !errors.Is(err, context.Canceled) || master != "" {
		t.Fatalf("resolveEnhancedHLS = %q, %v; want context.Canceled, not the web manifest", master, err)
	}
}
