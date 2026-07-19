package media

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"amdl/internal/applemusic"
	"amdl/internal/config"
	"amdl/internal/limits"
)

const qualityTestMaster = `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio-stereo-256",NAME="AAC"
#EXT-X-STREAM-INF:BANDWIDTH=281000,AVERAGE-BANDWIDTH=256000,CODECS="mp4a.40.2",AUDIO="audio-stereo-256"
audio.m3u8
`

type collectionQualityCatalog struct {
	mu sync.Mutex

	songs       map[string]applemusic.Song
	albums      map[string]applemusic.Collection
	playlist    applemusic.Collection
	station     applemusic.Collection
	artist      applemusic.ArtistAlbums
	resolveErr  error
	metadataErr map[string]error
	webHLS      map[string]string
	webErr      error

	songCalls       []string
	metadataCalls   []string
	albumCalls      []string
	playlistCalls   []string
	stationCalls    []string
	artistCalls     []string
	playlistTokens  []string
	stationTokens   []string
	webTokenHLSCall []string
}

func (f *collectionQualityCatalog) Song(_ context.Context, storefront, id string) (applemusic.Song, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.songCalls = append(f.songCalls, storefront+"/"+id)
	if f.resolveErr != nil {
		return applemusic.Song{}, f.resolveErr
	}
	return f.songs[id], nil
}

func (f *collectionQualityCatalog) SongMetadata(_ context.Context, storefront, id string) (applemusic.Song, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.metadataCalls = append(f.metadataCalls, storefront+"/"+id)
	if err := f.metadataErr[id]; err != nil {
		return applemusic.Song{}, err
	}
	return f.songs[id], nil
}

func (f *collectionQualityCatalog) Album(_ context.Context, storefront, id string) (applemusic.Collection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.albumCalls = append(f.albumCalls, storefront+"/"+id)
	if f.resolveErr != nil {
		return applemusic.Collection{}, f.resolveErr
	}
	return f.albums[id], nil
}

func (f *collectionQualityCatalog) Playlist(_ context.Context, storefront, id, token string) (applemusic.Collection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.playlistCalls = append(f.playlistCalls, storefront+"/"+id)
	f.playlistTokens = append(f.playlistTokens, token)
	if f.resolveErr != nil {
		return applemusic.Collection{}, f.resolveErr
	}
	return f.playlist, nil
}

func (f *collectionQualityCatalog) StationTracks(_ context.Context, storefront, id, token string) (applemusic.Collection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stationCalls = append(f.stationCalls, storefront+"/"+id)
	f.stationTokens = append(f.stationTokens, token)
	if f.resolveErr != nil {
		return applemusic.Collection{}, f.resolveErr
	}
	return f.station, nil
}

func (f *collectionQualityCatalog) ArtistAlbums(_ context.Context, storefront, id string) (applemusic.ArtistAlbums, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.artistCalls = append(f.artistCalls, storefront+"/"+id)
	if f.resolveErr != nil {
		return applemusic.ArtistAlbums{}, f.resolveErr
	}
	return f.artist, nil
}

func (f *collectionQualityCatalog) EnhancedHLSViaWebToken(_ context.Context, storefront, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.webTokenHLSCall = append(f.webTokenHLSCall, storefront+"/"+id)
	if f.webErr != nil {
		return "", f.webErr
	}
	return f.webHLS[id], nil
}

func newCollectionQualityCatalog(manifestBase string) *collectionQualityCatalog {
	return &collectionQualityCatalog{
		songs: map[string]applemusic.Song{
			"s1": {ID: "s1", Name: "One", ArtistName: "Artist", AlbumName: "First", HasLyrics: true, EnhancedHLS: manifestBase + "/s1.m3u8"},
			"s2": {ID: "s2", Name: "Two", ArtistName: "Artist", AlbumName: "First", EnhancedHLS: manifestBase + "/s2.m3u8"},
			"s3": {ID: "s3", Name: "Three", ArtistName: "Artist", AlbumName: "Second", EnhancedHLS: manifestBase + "/s3.m3u8"},
		},
		albums: map[string]applemusic.Collection{
			"a1": {ID: "a1", Tracks: []applemusic.Song{{ID: "s1", AlbumName: "First"}, {ID: "s2", AlbumName: "First"}}},
			"a2": {ID: "a2", Tracks: []applemusic.Song{{ID: "s2", AlbumName: "Second"}, {ID: "s3", AlbumName: "Second"}}},
		},
		playlist: applemusic.Collection{ID: "pl.1", Tracks: []applemusic.Song{{ID: "s2"}, {ID: "s1"}}},
		station:  applemusic.Collection{ID: "ra.1", Tracks: []applemusic.Song{{ID: "s1"}, {ID: "s3"}}},
		artist: applemusic.ArtistAlbums{Artist: applemusic.Artist{ID: "ar.1"}, Albums: []applemusic.Collection{
			{ID: "a1"}, {ID: "a2"},
		}},
		metadataErr: map[string]error{},
		webHLS: map[string]string{
			"s1": manifestBase + "/s1.m3u8", "s2": manifestBase + "/s2.m3u8", "s3": manifestBase + "/s3.m3u8",
		},
	}
}

func TestQualityQuerySupportsCollectionURLTypes(t *testing.T) {
	var manifestMu sync.Mutex
	manifestHits := map[string]int{}
	manifest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		manifestMu.Lock()
		manifestHits[r.URL.Path]++
		manifestMu.Unlock()
		_, _ = w.Write([]byte(qualityTestMaster))
	}))
	defer manifest.Close()

	tests := []struct {
		name      string
		url       string
		wantType  applemusic.URLType
		wantID    string
		wantSongs []string
		check     func(*testing.T, *collectionQualityCatalog)
	}{
		{name: "album", url: "https://music.apple.com/cn/album/example/a1", wantType: applemusic.TypeAlbum, wantID: "a1", wantSongs: []string{"s1", "s2"}, check: func(t *testing.T, f *collectionQualityCatalog) {
			if !reflect.DeepEqual(f.albumCalls, []string{"cn/a1"}) {
				t.Fatalf("album calls = %#v", f.albumCalls)
			}
		}},
		{name: "playlist", url: "https://beta.music.apple.com/cn/playlist/example/pl.1", wantType: applemusic.TypePlaylist, wantID: "pl.1", wantSongs: []string{"s2", "s1"}, check: func(t *testing.T, f *collectionQualityCatalog) {
			if !reflect.DeepEqual(f.playlistTokens, []string{"user-token"}) {
				t.Fatalf("playlist tokens = %#v", f.playlistTokens)
			}
		}},
		{name: "station", url: "https://music.apple.com/cn/station/example/ra.1", wantType: applemusic.TypeStation, wantID: "ra.1", wantSongs: []string{"s1", "s3"}, check: func(t *testing.T, f *collectionQualityCatalog) {
			if !reflect.DeepEqual(f.stationTokens, []string{"user-token"}) {
				t.Fatalf("station tokens = %#v", f.stationTokens)
			}
		}},
		{name: "artist", url: "https://classical.music.apple.com/cn/artist/example/ar.1", wantType: applemusic.TypeArtist, wantID: "ar.1", wantSongs: []string{"s1", "s2", "s2", "s3"}, check: func(t *testing.T, f *collectionQualityCatalog) {
			if !reflect.DeepEqual(f.artistCalls, []string{"cn/ar.1"}) || !reflect.DeepEqual(f.albumCalls, []string{"cn/a1", "cn/a2"}) {
				t.Fatalf("artist/album calls = %#v/%#v", f.artistCalls, f.albumCalls)
			}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			catalog := newCollectionQualityCatalog(manifest.URL)
			cfg := config.Default()
			cfg.Catalog.MediaUserToken = "  user-token  "
			service := NewQualityServiceWithCatalog(cfg, catalog)

			result, err := service.QueryQuality(context.Background(), QualityRequest{URL: tt.url})
			if err != nil {
				t.Fatal(err)
			}
			if result.Type != string(tt.wantType) || result.AdamID != tt.wantID || result.Song != nil || result.Qualities != nil {
				t.Fatalf("result header = %#v", result)
			}
			gotSongs := make([]string, len(result.Tracks))
			for i, track := range result.Tracks {
				gotSongs[i] = track.Song.ID
				if len(track.Qualities) != len(qualitySpecs) || !track.Qualities[0].Available {
					t.Fatalf("track %d qualities = %#v", i, track.Qualities)
				}
			}
			if !reflect.DeepEqual(gotSongs, tt.wantSongs) {
				t.Fatalf("track order = %#v, want %#v", gotSongs, tt.wantSongs)
			}
			if tt.name == "artist" && result.Tracks[2].Song.Album != "Second" {
				t.Fatalf("duplicate song lost occurrence album: %#v", result.Tracks[2].Song)
			}
			catalog.mu.Lock()
			if got, want := len(catalog.metadataCalls), len(uniqueStrings(tt.wantSongs)); got != want {
				catalog.mu.Unlock()
				t.Fatalf("SongMetadata calls = %d, want %d unique tracks", got, want)
			}
			tt.check(t, catalog)
			catalog.mu.Unlock()
		})
	}

	manifestMu.Lock()
	defer manifestMu.Unlock()
	if manifestHits["/s2.m3u8"] != 3 {
		t.Fatalf("s2 manifest hits = %d, want one per collection query despite artist duplicate", manifestHits["/s2.m3u8"])
	}
}

func TestQualityQueryAlbumTrackModePreservesSongResponse(t *testing.T) {
	manifest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(qualityTestMaster))
	}))
	defer manifest.Close()

	catalog := newCollectionQualityCatalog(manifest.URL)
	cfg := config.Default()
	cfg.Catalog.AlbumTrackURLMode = "song"
	service := NewQualityServiceWithCatalog(cfg, catalog)
	result, err := service.QueryQuality(context.Background(), QualityRequest{URL: "https://music.apple.com/cn/album/example/a1?i=s1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Type != "song" || result.AdamID != "s1" || result.Song == nil || result.Song.ID != "s1" || result.Tracks != nil {
		t.Fatalf("song-mode result = %#v", result)
	}
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"tracks"`) || !strings.Contains(string(body), `"song"`) || !strings.Contains(string(body), `"qualities"`) {
		t.Fatalf("single-song wire shape changed: %s", body)
	}

	cfg.Catalog.AlbumTrackURLMode = "album"
	service = NewQualityServiceWithCatalog(cfg, catalog)
	result, err = service.QueryQuality(context.Background(), QualityRequest{URL: "https://music.apple.com/cn/album/example/a1?i=s1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Type != "album" || len(result.Tracks) != 2 || result.Song != nil {
		t.Fatalf("album-mode result = %#v", result)
	}
}

func TestQualityQueryReadsLiveMediaUserToken(t *testing.T) {
	manifest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(qualityTestMaster))
	}))
	defer manifest.Close()
	catalog := newCollectionQualityCatalog(manifest.URL)
	catalog.station.Tracks[0].EnhancedHLS = manifest.URL + "/s1.m3u8"
	catalog.station.Tracks = catalog.station.Tracks[:1]
	cfg := config.Default()
	cfg.Catalog.MediaUserToken = "first"
	store := config.NewStore(cfg)
	service := &QualityService{store: store, catalog: catalog, http: newHTTPClient()}
	url := "https://music.apple.com/cn/station/example/ra.1"
	if _, err := service.QueryQuality(context.Background(), QualityRequest{URL: url}); err != nil {
		t.Fatal(err)
	}
	cfg.Catalog.MediaUserToken = " second "
	store.Set(cfg)
	if _, err := service.QueryQuality(context.Background(), QualityRequest{URL: url}); err != nil {
		t.Fatal(err)
	}
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	if !reflect.DeepEqual(catalog.stationTokens, []string{"first", "second"}) {
		t.Fatalf("station tokens = %#v", catalog.stationTokens)
	}
}

type collectionQualityWrapper struct {
	mu    sync.Mutex
	hls   map[string]string
	calls []string
}

func (w *collectionQualityWrapper) M3U8(_ context.Context, id string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, id)
	return w.hls[id], nil
}

func TestQualityQueryCollectionSignedManifestSources(t *testing.T) {
	manifest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(qualityTestMaster))
	}))
	defer manifest.Close()

	for _, source := range []string{"wrapper", "web_token"} {
		t.Run(source, func(t *testing.T) {
			catalog := newCollectionQualityCatalog(manifest.URL)
			cfg := config.Default()
			cfg.Catalog.AppleMusicPrivateKeyPath = "AuthKey.p8"
			cfg.Catalog.AppleMusicKeyID = "KEY"
			cfg.Catalog.AppleMusicTeamID = "TEAM"
			cfg.Catalog.SignedModeHLSSource = source
			service := NewQualityServiceWithCatalog(cfg, catalog)
			wrapper := &collectionQualityWrapper{hls: catalog.webHLS}
			service.wrapper = wrapper
			result, err := service.QueryQuality(context.Background(), QualityRequest{URL: "https://music.apple.com/cn/album/example/a1"})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Tracks) != 2 {
				t.Fatalf("tracks = %#v", result.Tracks)
			}
			catalog.mu.Lock()
			metadataCalls := append([]string(nil), catalog.metadataCalls...)
			webCalls := append([]string(nil), catalog.webTokenHLSCall...)
			catalog.mu.Unlock()
			wrapper.mu.Lock()
			wrapperCalls := append([]string(nil), wrapper.calls...)
			wrapper.mu.Unlock()
			if len(metadataCalls) != 0 {
				t.Fatalf("signed mode SongMetadata calls = %#v", metadataCalls)
			}
			if source == "wrapper" {
				if len(wrapperCalls) != 2 || len(webCalls) != 0 {
					t.Fatalf("wrapper/web calls = %#v/%#v", wrapperCalls, webCalls)
				}
			} else if len(webCalls) != 2 || len(wrapperCalls) != 0 {
				t.Fatalf("web/wrapper calls = %#v/%#v", webCalls, wrapperCalls)
			}
		})
	}
}

func TestQualityQueryRejectsUnsupportedAndEmptyCollections(t *testing.T) {
	catalog := newCollectionQualityCatalog("https://example.invalid")
	service := NewQualityServiceWithCatalog(config.Default(), catalog)
	if _, err := service.QueryQuality(context.Background(), QualityRequest{URL: "https://music.apple.com/cn/music-video/example/mv.1"}); err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("music-video error = %v", err)
	}
	catalog.albums["empty"] = applemusic.Collection{ID: "empty"}
	if _, err := service.QueryQuality(context.Background(), QualityRequest{URL: "https://music.apple.com/cn/album/example/empty"}); err == nil || !strings.Contains(err.Error(), "has no songs") {
		t.Fatalf("empty album error = %v", err)
	}
	catalog.metadataErr["s1"] = errors.New("metadata failed")
	if _, err := service.QueryQuality(context.Background(), QualityRequest{URL: "https://music.apple.com/cn/album/example/a1"}); err == nil || !strings.Contains(err.Error(), "metadata failed") {
		t.Fatalf("track error = %v", err)
	}
}

func TestQualityQueryUsesSharedRequestGateAndPreservesOrder(t *testing.T) {
	const maxParallelRequests = 3
	started := make(chan string, 8)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()
	manifest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), ".m3u8")
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte(qualityTestMaster))
	}))
	defer manifest.Close()

	catalog := newCollectionQualityCatalog(manifest.URL)
	tracks := make([]applemusic.Song, 8)
	for i := range tracks {
		id := "s" + string(rune('a'+i))
		tracks[i] = applemusic.Song{ID: id, Name: id}
		catalog.songs[id] = applemusic.Song{ID: id, Name: id, EnhancedHLS: manifest.URL + "/" + id + ".m3u8"}
	}
	catalog.albums["many"] = applemusic.Collection{ID: "many", Tracks: tracks}
	service := NewQualityServiceWithCatalog(config.Default(), catalog)
	service.requestGate = limits.NewRequestGate(maxParallelRequests, 1000, len(tracks))
	type queryResult struct {
		result QualityResult
		err    error
	}
	done := make(chan queryResult, 1)
	go func() {
		result, err := service.QueryQuality(context.Background(), QualityRequest{URL: "https://music.apple.com/cn/album/example/many"})
		done <- queryResult{result: result, err: err}
	}()
	for range maxParallelRequests {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for shared request-gate slots")
		}
	}
	select {
	case id := <-started:
		t.Fatalf("more than %d manifest requests started before release: %s", maxParallelRequests, id)
	case <-time.After(100 * time.Millisecond):
	}
	releaseAll()
	query := <-done
	if query.err != nil {
		t.Fatal(query.err)
	}
	for i, track := range query.result.Tracks {
		if track.Song.ID != tracks[i].ID {
			t.Fatalf("track %d = %s, want %s", i, track.Song.ID, tracks[i].ID)
		}
	}
}

func TestQualityQueryCancelsSiblingProbesAfterFailure(t *testing.T) {
	blockedStarted := make(chan struct{})
	blockedCanceled := make(chan struct{})
	manifest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/failed.m3u8":
			select {
			case <-blockedStarted:
			case <-r.Context().Done():
				return
			}
			http.Error(w, "upstream failed", http.StatusBadGateway)
		case "/blocked.m3u8":
			close(blockedStarted)
			<-r.Context().Done()
			close(blockedCanceled)
		}
	}))
	defer manifest.Close()

	catalog := newCollectionQualityCatalog(manifest.URL)
	catalog.albums["cancel"] = applemusic.Collection{ID: "cancel", Tracks: []applemusic.Song{{ID: "failed"}, {ID: "blocked"}}}
	catalog.songs["failed"] = applemusic.Song{ID: "failed", EnhancedHLS: manifest.URL + "/failed.m3u8"}
	catalog.songs["blocked"] = applemusic.Song{ID: "blocked", EnhancedHLS: manifest.URL + "/blocked.m3u8"}
	service := NewQualityServiceWithCatalog(config.Default(), catalog)
	_, err := service.QueryQuality(context.Background(), QualityRequest{URL: "https://music.apple.com/cn/album/example/cancel"})
	if err == nil || !strings.Contains(err.Error(), "502 Bad Gateway") {
		t.Fatalf("query error = %v", err)
	}
	select {
	case <-blockedCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("sibling manifest request was not canceled")
	}
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}
