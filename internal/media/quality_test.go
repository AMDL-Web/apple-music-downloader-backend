package media

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
	service := NewQualityServiceWithCatalog(cfg, fakeQualityCatalog{song: applemusic.Song{
		ID: "song-1", Name: "Song", ArtistName: "Artist", AlbumName: "Album", EnhancedHLS: manifest.URL + "/master.m3u8",
	}}, countingQualityWrapper{})
	result, err := service.QueryQuality(context.Background(), QualityRequest{URL: "https://music.apple.com/cn/song/example/song-1"})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]QualityOption{
		"aac":  {Available: true, CodecID: "audio-stereo-256", Bitrate: 256000},
		"alac": {Available: true, CodecID: "audio-alac-stereo-96000-24", Bitrate: 3900000, BitDepth: 24, SampleRate: 96000},
		"ec3":  {Available: true, CodecID: "audio-atmos-2448", Bitrate: 640000},
	}
	got := map[string]QualityOption{}
	wantOrder := []string{"aac", "aac-binaural", "aac-downmix", "alac", "ec3", "ac3"}
	for i, quality := range result.Qualities {
		if quality.ID != wantOrder[i] {
			t.Fatalf("quality order[%d] = %q, want %q", i, quality.ID, wantOrder[i])
		}
		got[quality.ID] = quality
	}
	for id, expected := range want {
		actual, ok := got[id]
		if !ok {
			t.Fatalf("quality %q missing from %#v", id, result.Qualities)
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
	song              applemusic.Song
	webTokenHLS       string
	webTokenErr       error
	webTokenCallCount *int
	wrapperM3U8Calls  *int
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

func (f fakeQualityCatalog) EnhancedHLSViaWebToken(context.Context, string, string) (string, error) {
	if f.webTokenCallCount != nil {
		*f.webTokenCallCount++
	}
	return f.webTokenHLS, f.webTokenErr
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
	return wrapper.Status{Ready: true, Status: true, Regions: []string{"cn"}}, nil
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
		{name: "unsupported storefront", wrapper: countingQualityWrapper{status: &wrapper.Status{Ready: true, Status: true, Regions: []string{"us"}}}, wantCode: "unsupported_storefront"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := NewQualityServiceWithCatalog(cfg, catalog, tt.wrapper)
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

	service := NewQualityServiceWithCatalog(config.Default(), fakeQualityCatalog{song: applemusic.Song{
		ID: "song-1", Name: "Song", ArtistName: "Artist", AlbumName: "Album", HasLyrics: true, EnhancedHLS: manifest.URL + "/master.m3u8",
	}}, countingQualityWrapper{})

	result, err := service.QueryQuality(context.Background(), QualityRequest{URL: "https://music.apple.com/cn/song/example/song-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Qualities) == 0 || !result.Qualities[0].Available {
		t.Fatalf("expected catalog manifest qualities, got %#v", result.Qualities)
	}
	if !result.Song.HasLyrics {
		t.Fatalf("song = %+v, want has_lyrics propagated from catalog", result.Song)
	}
}

func TestQualityQuerySignedModeUsesWebTokenSource(t *testing.T) {
	manifest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		writeQualityTestPlaylist(w, r, `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio-stereo-256",NAME="AAC"
#EXT-X-STREAM-INF:BANDWIDTH=281000,AVERAGE-BANDWIDTH=256000,CODECS="mp4a.40.2",AUDIO="audio-stereo-256"
audio.m3u8
`)
	}))
	defer manifest.Close()

	cfg := config.Default()
	cfg.Catalog.AppleMusicPrivateKeyPath = "keys/AuthKey.p8"
	cfg.Catalog.AppleMusicKeyID = "88KBJL3CKU"
	cfg.Catalog.AppleMusicTeamID = "2VTXNMR2GL"
	cfg.Catalog.SignedModeHLSSource = "web_token"

	var webCalls, wrapperCalls int
	catalog := fakeQualityCatalog{
		song:              applemusic.Song{ID: "song-1", Name: "Song", ArtistName: "Artist", AlbumName: "Album"},
		webTokenHLS:       manifest.URL + "/master.m3u8",
		webTokenCallCount: &webCalls,
	}
	service := NewQualityServiceWithCatalog(cfg, catalog, countingQualityWrapper{m3u8: "https://wrapper.invalid/master.m3u8", callCount: &wrapperCalls})

	result, err := service.QueryQuality(context.Background(), QualityRequest{URL: "https://music.apple.com/cn/song/example/song-1"})
	if err != nil {
		t.Fatal(err)
	}
	if webCalls != 1 {
		t.Fatalf("EnhancedHLSViaWebToken calls = %d, want one master inventory", webCalls)
	}
	if wrapperCalls != 0 {
		t.Fatalf("wrapper.M3U8 calls = %d, want 0", wrapperCalls)
	}
	if len(result.Qualities) == 0 || !result.Qualities[0].Available {
		t.Fatalf("expected web-token manifest qualities, got %#v", result.Qualities)
	}
}

func TestQualityQuerySignedModeUsesWrapperByDefault(t *testing.T) {
	manifest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		writeQualityTestPlaylist(w, r, `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio-stereo-256",NAME="AAC"
#EXT-X-STREAM-INF:BANDWIDTH=281000,AVERAGE-BANDWIDTH=256000,CODECS="mp4a.40.2",AUDIO="audio-stereo-256"
audio.m3u8
`)
	}))
	defer manifest.Close()

	cfg := config.Default()
	cfg.Catalog.AppleMusicPrivateKeyPath = "keys/AuthKey.p8"
	cfg.Catalog.AppleMusicKeyID = "88KBJL3CKU"
	cfg.Catalog.AppleMusicTeamID = "2VTXNMR2GL"
	// SignedModeHLSSource default is wrapper

	var webCalls, wrapperCalls int
	catalog := fakeQualityCatalog{
		song:              applemusic.Song{ID: "song-1", Name: "Song", ArtistName: "Artist", AlbumName: "Album"},
		webTokenCallCount: &webCalls,
	}
	service := NewQualityServiceWithCatalog(cfg, catalog, countingQualityWrapper{m3u8: manifest.URL + "/master.m3u8", callCount: &wrapperCalls})

	result, err := service.QueryQuality(context.Background(), QualityRequest{URL: "https://music.apple.com/cn/song/example/song-1"})
	if err != nil {
		t.Fatal(err)
	}
	if wrapperCalls != 1 {
		t.Fatalf("wrapper.M3U8 calls = %d, want one master inventory", wrapperCalls)
	}
	if webCalls != 0 {
		t.Fatalf("EnhancedHLSViaWebToken calls = %d, want 0", webCalls)
	}
	if len(result.Qualities) == 0 || !result.Qualities[0].Available {
		t.Fatalf("expected wrapper manifest qualities, got %#v", result.Qualities)
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
	cfg.Catalog.AppleMusicPrivateKeyPath = "keys/AuthKey.p8"
	cfg.Catalog.AppleMusicKeyID = "KEY"
	cfg.Catalog.AppleMusicTeamID = "TEAM"
	var wrapperCalls int
	service := NewQualityServiceWithCatalog(cfg, fakeQualityCatalog{song: applemusic.Song{
		ID: "song-1", Name: "Song", ArtistName: "Artist", AlbumName: "Album",
	}}, countingQualityWrapper{m3u8: manifest.URL + "/master.m3u8", callCount: &wrapperCalls})

	result, err := service.QueryQuality(context.Background(), QualityRequest{URL: "https://music.apple.com/cn/song/example/song-1"})
	if err != nil {
		t.Fatal(err)
	}
	if wrapperCalls != 2 || masterHits.Load() != 2 {
		t.Fatalf("retry calls = wrapper:%d master:%d, want wrapper:2 master:2", wrapperCalls, masterHits.Load())
	}
	if len(result.Qualities) == 0 || !result.Qualities[0].Available {
		t.Fatalf("qualities after retry = %#v", result.Qualities)
	}
}
