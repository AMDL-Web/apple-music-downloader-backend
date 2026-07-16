package applemusic

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"amdl/internal/config"
)

func TestFormatArtworkURL(t *testing.T) {
	raw := "https://is1-ssl.mzstatic.com/image/thumb/foo/{w}x{h}bb.jpg"
	got := formatArtworkURL(raw, "jpg", "1400x1400")
	want := "https://is1-ssl.mzstatic.com/image/thumb/foo/1400x1400bb.jpg"
	if got != want {
		t.Fatalf("formatArtworkURL() = %q, want %q", got, want)
	}
}

func TestReadLimitedRejectsOversizedUpstreamResponse(t *testing.T) {
	if _, err := readLimited(strings.NewReader("12345"), 4); err == nil {
		t.Fatal("readLimited accepted an upstream response larger than its limit")
	}
}

func TestFormatArtworkURLPNG(t *testing.T) {
	raw := "https://is1-ssl.mzstatic.com/image/thumb/foo/{w}x{h}bb.jpg"
	got := formatArtworkURL(raw, "png", "600x600")
	want := "https://is1-ssl.mzstatic.com/image/thumb/foo/600x600bb.png"
	if got != want {
		t.Fatalf("formatArtworkURL() = %q, want %q", got, want)
	}
}

func TestCoverSizeFallbacks(t *testing.T) {
	got := coverSizeFallbacks("5000x5000")
	if len(got) < 2 || got[0] != "5000x5000" {
		t.Fatalf("coverSizeFallbacks() = %#v", got)
	}
}

func TestFetchCoverFallsBackToSmallerSize(t *testing.T) {
	client := NewCatalogClient(config.CatalogConfig{}, slog.Default())
	raw := "https://is1-ssl.mzstatic.com/image/thumb/foo/{w}x{h}bb.jpg"
	client.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.String(), "5000x5000") {
			return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
		}
		if strings.Contains(req.URL.String(), "1400x1400") {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("cover-bytes")), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}

	data, err := client.FetchCover(context.Background(), []string{raw}, "jpg", "5000x5000")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "cover-bytes" {
		t.Fatalf("FetchCover() = %q", data)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestSongRequestsAndMapsExtendedAssetURLs(t *testing.T) {
	client := NewCatalogClient(config.CatalogConfig{Language: "en-US"}, slog.Default())
	client.token = "test-token"
	client.tokenUntil = time.Now().Add(time.Hour)
	client.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.URL.Query().Get("extend"); got != "extendedAssetUrls" {
			t.Fatalf("extend query = %q, want extendedAssetUrls", got)
		}
		body := `{"data":[{"id":"123","type":"songs","attributes":{"name":"Song","artistName":"Artist","albumName":"Album","extendedAssetUrls":{"enhancedHls":"https://example.test/master.m3u8"}},"relationships":{}}]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}

	song, err := client.Song(context.Background(), "cn", "123")
	if err != nil {
		t.Fatal(err)
	}
	if song.EnhancedHLS != "https://example.test/master.m3u8" {
		t.Fatalf("EnhancedHLS = %q", song.EnhancedHLS)
	}
}

func TestCatalogRequestErrorClassificationAndRetryAfter(t *testing.T) {
	if !(catalogRequestError{statusCode: http.StatusNotFound}).NonRetryable() {
		t.Fatal("404 should be non-retryable")
	}
	if (catalogRequestError{statusCode: http.StatusTooManyRequests}).NonRetryable() {
		t.Fatal("429 should remain retryable")
	}
	now := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	if got := parseRetryAfter("3", now); got != 3*time.Second {
		t.Fatalf("delta Retry-After=%s, want 3s", got)
	}
	if got := parseRetryAfter(now.Add(5*time.Second).Format(http.TimeFormat), now); got != 5*time.Second {
		t.Fatalf("date Retry-After=%s, want 5s", got)
	}
}

func TestSongMetadataDoesNotFollowAlbumRelationship(t *testing.T) {
	client := NewCatalogClient(config.CatalogConfig{Language: "en-US"}, slog.Default())
	client.token = "test-token"
	client.tokenUntil = time.Now().Add(time.Hour)
	var paths []string
	client.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		paths = append(paths, req.URL.Path)
		body := `{"data":[{"id":"song-1","type":"songs","attributes":{"name":"Song","artistName":"Artist","albumName":"Album"},"relationships":{"albums":{"data":[{"id":"album-1","type":"albums","attributes":{"name":"Album","artistName":"Album Artist","trackCount":10}}]}}}]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}

	song, err := client.SongMetadata(context.Background(), "cn", "song-1")
	if err != nil {
		t.Fatal(err)
	}
	if song.AlbumID != "album-1" || song.TrackCount != 10 {
		t.Fatalf("song metadata = %+v, want mapped album relationship", song)
	}
	if got, want := paths, []string{"/v1/catalog/cn/songs/song-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("request paths = %#v, want %#v", got, want)
	}
}

func TestSongStillFollowsAlbumRelationshipForFullMetadata(t *testing.T) {
	client := NewCatalogClient(config.CatalogConfig{Language: "en-US"}, slog.Default())
	client.token = "test-token"
	client.tokenUntil = time.Now().Add(time.Hour)
	var paths []string
	client.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		paths = append(paths, req.URL.Path)
		switch req.URL.Path {
		case "/v1/catalog/cn/songs/song-1":
			body := `{"data":[{"id":"song-1","type":"songs","attributes":{"name":"Song","artistName":"Artist","albumName":"Album","trackNumber":1,"discNumber":1},"relationships":{"albums":{"data":[{"id":"album-1","type":"albums","attributes":{"name":"Album","artistName":"Album Artist","trackCount":2}}]}}}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		case "/v1/catalog/cn/albums/album-1":
			body := `{"data":[{"id":"album-1","type":"albums","attributes":{"name":"Album","artistName":"Album Artist","trackCount":2,"artwork":{"url":"album-art"}},"relationships":{"artists":{"data":[{"id":"artist-1","attributes":{"name":"Album Artist","artwork":{"url":"artist-art"}}}]},"tracks":{"data":[{"id":"song-1","type":"songs","attributes":{"name":"Song","trackNumber":1,"discNumber":1}},{"id":"song-2","type":"songs","attributes":{"name":"Two","trackNumber":2,"discNumber":1}}]}}}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("unexpected path")), Header: make(http.Header)}, nil
		}
	})}

	song, err := client.Song(context.Background(), "cn", "song-1")
	if err != nil {
		t.Fatal(err)
	}
	if song.AlbumArtist != "Album Artist" || song.AlbumArtworkURL != "album-art" || song.AlbumArtistID != "artist-1" || song.AlbumArtistArtworkURL != "artist-art" || song.TrackCount != 2 || song.DiscCount != 1 {
		t.Fatalf("enriched song = %+v", song)
	}
	if got, want := paths, []string{"/v1/catalog/cn/songs/song-1", "/v1/catalog/cn/albums/album-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("request paths = %#v, want %#v", got, want)
	}
}

func TestAlbumFetchesAllTrackPages(t *testing.T) {
	client := NewCatalogClient(config.CatalogConfig{Language: "en-US"}, slog.Default())
	client.token = "test-token"
	client.tokenUntil = time.Now().Add(time.Hour)
	var paths []string
	client.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		paths = append(paths, req.URL.RequestURI())
		if req.URL.Path == "/v1/catalog/cn/albums/album-1" {
			body := `{"data":[{"id":"album-1","type":"albums","attributes":{"name":"Album","artistName":"Album Artist","trackCount":3},"relationships":{"artists":{"data":[{"id":"artist-1","attributes":{"name":"Album Artist","artwork":{"url":"artist-art"}}}]},"tracks":{"data":[{"id":"song-1","type":"songs","attributes":{"name":"One","artistName":"Artist","albumName":"Album","trackNumber":1,"discNumber":1}},{"id":"video-1","type":"music-videos","attributes":{"name":"Video"}}],"next":"/v1/catalog/cn/albums/album-1/tracks?offset=1"}}}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		}
		if req.URL.Path == "/v1/catalog/cn/albums/album-1/tracks" {
			body := `{"data":[{"id":"song-2","type":"songs","attributes":{"name":"Two","artistName":"Artist","albumName":"Album","trackNumber":2,"discNumber":1}},{"id":"video-2","type":"music-videos","attributes":{"name":"Video Two"}},{"id":"song-3","type":"songs","attributes":{"name":"Three","artistName":"Artist","albumName":"Album","trackNumber":3,"discNumber":2}}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("unexpected path: " + req.URL.RequestURI())), Header: make(http.Header)}, nil
	})}

	album, err := client.Album(context.Background(), "cn", "album-1")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := trackIDs(album.Tracks), []string{"song-1", "song-2", "song-3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("track IDs = %#v, want %#v", got, want)
	}
	if got, want := album.Tracks[2].DiscCount, 2; got != want {
		t.Fatalf("last track DiscCount = %d, want %d", got, want)
	}
	if len(paths) != 2 || paths[1] != "/v1/catalog/cn/albums/album-1/tracks?offset=1" {
		t.Fatalf("request paths = %#v", paths)
	}
}

func TestPlaylistFetchesAllTrackPages(t *testing.T) {
	client := NewCatalogClient(config.CatalogConfig{Language: "en-US"}, slog.Default())
	client.token = "test-token"
	client.tokenUntil = time.Now().Add(time.Hour)
	var paths []string
	client.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		paths = append(paths, req.URL.RequestURI())
		if req.URL.Path == "/v1/catalog/cn/playlists/pl.1" {
			body := `{"data":[{"id":"pl.1","type":"playlists","attributes":{"name":"Playlist","curatorName":"Curator","artwork":{"url":"playlist-art"}},"relationships":{"tracks":{"data":[{"id":"song-1","type":"songs","attributes":{"name":"One","artistName":"Artist"}}],"next":"/v1/catalog/cn/playlists/pl.1/tracks?offset=1"}}}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		}
		if req.URL.Path == "/v1/catalog/cn/playlists/pl.1/tracks" {
			body := `{"data":[{"id":"song-2","type":"songs","attributes":{"name":"Two","artistName":"Artist"}},{"id":"video-1","type":"music-videos","attributes":{"name":"Video"}},{"id":"song-3","type":"songs","attributes":{"name":"Three","artistName":"Artist"}}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("unexpected path: " + req.URL.RequestURI())), Header: make(http.Header)}, nil
	})}

	playlist, err := client.Playlist(context.Background(), "cn", "pl.1", "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := trackIDs(playlist.Tracks), []string{"song-1", "song-2", "song-3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("track IDs = %#v, want %#v", got, want)
	}
	if len(paths) != 2 || paths[1] != "/v1/catalog/cn/playlists/pl.1/tracks?offset=1" {
		t.Fatalf("request paths = %#v", paths)
	}
}

func TestPlaylistPrivateCoverFallsBackToLibraryArtwork(t *testing.T) {
	client := NewCatalogClient(config.CatalogConfig{Language: "zh-Hans_CN"}, slog.Default())
	client.token = "test-token"
	client.tokenUntil = time.Now().Add(time.Hour)
	type seen struct{ mediaToken, include string }
	var requests []seen
	client.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, seen{req.Header.Get("Media-User-Token"), req.URL.Query().Get("include")})
		body := `{"data":[{"id":"pl.u-1","type":"playlists","attributes":{"name":"Private","artwork":{"url":""}},"relationships":{"tracks":{"data":[{"id":"song-1","type":"songs","attributes":{"name":"One","artistName":"Artist"}}]},"library":{"data":[{"id":"p.lib1","type":"library-playlists","attributes":{"name":"Private","artwork":{"url":"https://blob.example/image?sig=abc"}}}]}}}]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}

	playlist, err := client.Playlist(context.Background(), "cn", "pl.u-1", "media-token-xyz")
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2 (plain fetch + library enrichment)", len(requests))
	}
	if requests[0].mediaToken != "" || requests[0].include != "" {
		t.Fatalf("plain fetch leaked auth: %+v", requests[0])
	}
	if requests[1].mediaToken != "media-token-xyz" || requests[1].include != "library" {
		t.Fatalf("enrichment request = %+v, want token + include=library", requests[1])
	}
	if playlist.ArtworkURL != "https://blob.example/image?sig=abc" {
		t.Fatalf("ArtworkURL = %q, want library artwork", playlist.ArtworkURL)
	}
}

func TestPlaylistCatalogArtworkSkipsLibraryLookup(t *testing.T) {
	client := NewCatalogClient(config.CatalogConfig{Language: "zh-Hans_CN"}, slog.Default())
	client.token = "test-token"
	client.tokenUntil = time.Now().Add(time.Hour)
	var calls int
	client.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		body := `{"data":[{"id":"pl.u-1","type":"playlists","attributes":{"name":"Shared","artwork":{"url":"catalog-art"}},"relationships":{"tracks":{"data":[]}}}]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}

	playlist, err := client.Playlist(context.Background(), "cn", "pl.u-1", "media-token-xyz")
	if err != nil {
		t.Fatal(err)
	}
	if playlist.ArtworkURL != "catalog-art" {
		t.Fatalf("ArtworkURL = %q, want catalog artwork to win", playlist.ArtworkURL)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (no enrichment when catalog artwork exists)", calls)
	}
}

func TestPlaylistPublicIDWithTokenSkipsEnrichment(t *testing.T) {
	client := NewCatalogClient(config.CatalogConfig{Language: "zh-Hans_CN"}, slog.Default())
	client.token = "test-token"
	client.tokenUntil = time.Now().Add(time.Hour)
	var calls int
	client.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		body := `{"data":[{"id":"pl.123","type":"playlists","attributes":{"name":"Editorial","artwork":{"url":""}},"relationships":{"tracks":{"data":[]}}}]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}

	playlist, err := client.Playlist(context.Background(), "cn", "pl.123", "media-token-xyz")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (non-private ids never trigger enrichment)", calls)
	}
	if playlist.ArtworkURL != "" {
		t.Fatalf("ArtworkURL = %q, want empty", playlist.ArtworkURL)
	}
}

func TestPlaylistLibraryLookupFailureIsNonFatal(t *testing.T) {
	client := NewCatalogClient(config.CatalogConfig{Language: "zh-Hans_CN"}, slog.Default())
	client.token = "test-token"
	client.tokenUntil = time.Now().Add(time.Hour)
	var calls int
	client.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 2 {
			// Enrichment request rejected, e.g. an expired media-user-token.
			return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader(`{"errors":[{"status":"403"}]}`)), Header: make(http.Header)}, nil
		}
		body := `{"data":[{"id":"pl.u-1","type":"playlists","attributes":{"name":"Private","artwork":{"url":""}},"relationships":{"tracks":{"data":[{"id":"song-1","type":"songs","attributes":{"name":"One","artistName":"Artist"}}]}}}]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}

	playlist, err := client.Playlist(context.Background(), "cn", "pl.u-1", "expired-token")
	if err != nil {
		t.Fatalf("playlist resolve must survive a failed artwork enrichment, got %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if playlist.ArtworkURL != "" {
		t.Fatalf("ArtworkURL = %q, want empty after failed enrichment", playlist.ArtworkURL)
	}
	if len(playlist.Tracks) != 1 {
		t.Fatalf("tracks = %d, want 1", len(playlist.Tracks))
	}
}

func TestPlaylistWithoutTokenSkipsLibraryInclude(t *testing.T) {
	client := NewCatalogClient(config.CatalogConfig{Language: "zh-Hans_CN"}, slog.Default())
	client.token = "test-token"
	client.tokenUntil = time.Now().Add(time.Hour)
	var sawMediaToken bool
	var gotInclude string
	client.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		sawMediaToken = req.Header.Get("Media-User-Token") != ""
		gotInclude = req.URL.Query().Get("include")
		body := `{"data":[{"id":"pl.1","type":"playlists","attributes":{"name":"Editorial","artwork":{"url":"catalog-art"}},"relationships":{"tracks":{"data":[]}}}]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}

	if _, err := client.Playlist(context.Background(), "cn", "pl.1", ""); err != nil {
		t.Fatal(err)
	}
	if sawMediaToken || gotInclude != "" {
		t.Fatalf("tokenless playlist request leaked auth: header=%v include=%q", sawMediaToken, gotInclude)
	}
}

func TestStationTracksResolvesTracksFormat(t *testing.T) {
	client := NewCatalogClient(config.CatalogConfig{Language: "en-US"}, slog.Default())
	client.token = "test-token"
	client.tokenUntil = time.Now().Add(time.Hour)
	var gotMediaToken string
	var nextTracksMethod string
	client.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/v1/catalog/us/stations/ra.1":
			body := `{"data":[{"id":"ra.1","type":"stations","attributes":{"name":"My Station","isLive":false,"artwork":{"url":"station-art"},"playParams":{"format":"tracks"}}}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		case "/v1/me/stations/next-tracks/ra.1":
			gotMediaToken = req.Header.Get("Media-User-Token")
			nextTracksMethod = req.Method
			body := `{"data":[{"id":"song-1","type":"songs","attributes":{"name":"One","artistName":"Artist"}},{"id":"video-1","type":"music-videos","attributes":{"name":"Video"}},{"id":"song-2","type":"songs","attributes":{"name":"Two","artistName":"Artist"}}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("unexpected path: " + req.URL.RequestURI())), Header: make(http.Header)}, nil
	})}

	station, err := client.StationTracks(context.Background(), "us", "ra.1", "media-token-xyz")
	if err != nil {
		t.Fatal(err)
	}
	if station.Type != TypeStation || station.Name != "My Station" || station.ArtworkURL != "station-art" {
		t.Fatalf("station = %+v", station)
	}
	if got, want := trackIDs(station.Tracks), []string{"song-1", "song-2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("track IDs = %#v, want %#v", got, want)
	}
	if gotMediaToken != "media-token-xyz" {
		t.Fatalf("Media-User-Token header = %q", gotMediaToken)
	}
	if nextTracksMethod != http.MethodPost {
		t.Fatalf("next-tracks method = %q, want POST", nextTracksMethod)
	}
}

func TestStationTracksRejectsLiveStream(t *testing.T) {
	client := NewCatalogClient(config.CatalogConfig{Language: "en-US"}, slog.Default())
	client.token = "test-token"
	client.tokenUntil = time.Now().Add(time.Hour)
	client.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/v1/catalog/us/stations/ra.live" {
			body := `{"data":[{"id":"ra.live","type":"stations","attributes":{"name":"Apple Music 1","isLive":true,"playParams":{"format":"stream"}}}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		}
		t.Fatalf("unexpected request to %s (live station must not hit next-tracks)", req.URL.RequestURI())
		return nil, nil
	})}

	if _, err := client.StationTracks(context.Background(), "us", "ra.live", "media-token-xyz"); err == nil {
		t.Fatal("expected live station to be rejected")
	}
}

func TestStationTracksRequiresMediaUserToken(t *testing.T) {
	client := NewCatalogClient(config.CatalogConfig{Language: "en-US"}, slog.Default())
	client.token = "test-token"
	client.tokenUntil = time.Now().Add(time.Hour)
	client.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/v1/catalog/us/stations/ra.1" {
			body := `{"data":[{"id":"ra.1","type":"stations","attributes":{"name":"My Station","playParams":{"format":"tracks"}}}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		}
		t.Fatalf("unexpected request to %s (missing token must not hit next-tracks)", req.URL.RequestURI())
		return nil, nil
	})}

	if _, err := client.StationTracks(context.Background(), "us", "ra.1", "  "); err == nil {
		t.Fatal("expected missing media_user_token to be rejected")
	}
}

func TestArtistAlbumsFetchesIncludedAlbumsAndNextPages(t *testing.T) {
	client := NewCatalogClient(config.CatalogConfig{Language: "zh-Hans_CN"}, slog.Default())
	client.token = "test-token"
	client.tokenUntil = time.Now().Add(time.Hour)
	var paths []string
	client.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		paths = append(paths, req.URL.RequestURI())
		if req.URL.Path == "/v1/catalog/cn/artists/artist-1" {
			if got := req.URL.Query().Get("include"); got != "albums" {
				t.Fatalf("include query = %q, want albums", got)
			}
			body := `{"data":[{"id":"artist-1","type":"artists","attributes":{"name":"Artist","artwork":{"url":"artist-art"}},"relationships":{"albums":{"data":[{"id":"album-1","type":"albums","attributes":{"name":"First","artistName":"Artist","artwork":{"url":"first-art"},"releaseDate":"2024-01-01","trackCount":2}},{"id":"video-1","type":"music-videos","attributes":{"name":"Video"}},{"id":"album-2","type":"albums","attributes":{"name":"Second","artistName":"Artist","artwork":{"url":"second-art"},"releaseDate":"2024-02-01","trackCount":1}}],"next":"/v1/catalog/cn/artists/artist-1/albums?offset=2"}}}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		}
		if req.URL.Path == "/v1/catalog/cn/artists/artist-1/albums" {
			body := `{"data":[{"id":"album-2","type":"albums","attributes":{"name":"Second Duplicate","artistName":"Artist"}},{"id":"album-3","type":"albums","attributes":{"name":"Third","artistName":"Artist","artwork":{"url":"third-art"},"releaseDate":"2024-03-01","trackCount":3}}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("unexpected path: " + req.URL.RequestURI())), Header: make(http.Header)}, nil
	})}

	artist, err := client.ArtistAlbums(context.Background(), "cn", "artist-1")
	if err != nil {
		t.Fatal(err)
	}
	if artist.ID != "artist-1" || artist.Name != "Artist" || artist.ArtworkURL != "artist-art" {
		t.Fatalf("artist metadata = %+v, want id/name/artwork from artist payload", artist.Artist)
	}
	if got, want := albumIDs(artist.Albums), []string{"album-1", "album-2", "album-3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("album IDs = %#v, want %#v", got, want)
	}
	if artist.Albums[2].Name != "Third" || artist.Albums[2].Artist != "Artist" || artist.Albums[2].ArtworkURL != "third-art" {
		t.Fatalf("third album metadata = %+v", artist.Albums[2])
	}
	if len(paths) != 2 || paths[1] != "/v1/catalog/cn/artists/artist-1/albums?offset=2" {
		t.Fatalf("request paths = %#v", paths)
	}
}

func trackIDs(tracks []Song) []string {
	out := make([]string, 0, len(tracks))
	for _, track := range tracks {
		out = append(out, track.ID)
	}
	return out
}

func albumIDs(albums []Collection) []string {
	out := make([]string, 0, len(albums))
	for _, album := range albums {
		out = append(out, album.ID)
	}
	return out
}

func TestCacheUntilIsHalfLife(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tests := []struct {
		name string
		ttl  time.Duration
	}{
		{"signed 24h internal ttl", internalDeveloperTokenTTL},
		{"legacy 12h default", 12 * time.Hour},
		{"legacy custom 6h", 6 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cacheUntil(now, tt.ttl)
			want := now.Add(tt.ttl / 2)
			if !got.Equal(want) {
				t.Fatalf("cacheUntil(%v, %v) = %v, want %v", now, tt.ttl, got, want)
			}
		})
	}
}

func TestInitDeveloperTokenCachesForHalfTheSignedLifetime(t *testing.T) {
	path, _ := writeP8Key(t)
	cfg := config.CatalogConfig{
		AppleMusicPrivateKeyPath: path,
		AppleMusicKeyID:          "KID1234567",
		AppleMusicTeamID:         "TEAM123456",
	}
	client := NewCatalogClient(cfg, slog.Default())

	before := time.Now()
	if err := client.InitDeveloperToken(); err != nil {
		t.Fatalf("InitDeveloperToken: %v", err)
	}
	after := time.Now()

	minUntil := before.Add(internalDeveloperTokenTTL / 2)
	maxUntil := after.Add(internalDeveloperTokenTTL / 2)
	if client.tokenUntil.Before(minUntil) || client.tokenUntil.After(maxUntil) {
		t.Fatalf("tokenUntil = %v, want within [%v, %v] (half of %v, not exp-5m)", client.tokenUntil, minUntil, maxUntil, internalDeveloperTokenTTL)
	}
}

func TestTokenRefreshUsesHalfLifeForBothModes(t *testing.T) {
	t.Run("signed", func(t *testing.T) {
		path, _ := writeP8Key(t)
		cfg := config.CatalogConfig{
			AppleMusicPrivateKeyPath: path,
			AppleMusicKeyID:          "KID1234567",
			AppleMusicTeamID:         "TEAM123456",
		}
		client := NewCatalogClient(cfg, slog.Default())
		client.signer, _ = newDeveloperTokenSigner(path, "KID1234567", "TEAM123456")
		// Force a refresh by expiring the cached token.
		client.token = "stale"
		client.tokenUntil = time.Now().Add(-time.Minute)

		before := time.Now()
		if _, err := client.Token(context.Background()); err != nil {
			t.Fatalf("Token: %v", err)
		}
		after := time.Now()

		minUntil := before.Add(internalDeveloperTokenTTL / 2)
		maxUntil := after.Add(internalDeveloperTokenTTL / 2)
		if client.tokenUntil.Before(minUntil) || client.tokenUntil.After(maxUntil) {
			t.Fatalf("tokenUntil = %v, want within [%v, %v]", client.tokenUntil, minUntil, maxUntil)
		}
	})

	t.Run("legacy", func(t *testing.T) {
		client := NewCatalogClient(config.CatalogConfig{TokenCacheTTLHours: 12}, slog.Default())
		client.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.String() == "https://music.apple.com" {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`<script src="/assets/index~abc123.js"></script>`)), Header: make(http.Header)}, nil
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`token:"eyJhbGciOiJFUzI1NiJ9.eyJmb28iOiJiYXIifQ.sig-part-here"`)), Header: make(http.Header)}, nil
		})}

		before := time.Now()
		if _, err := client.Token(context.Background()); err != nil {
			t.Fatalf("Token: %v", err)
		}
		after := time.Now()

		wantTTL := client.cfg.TokenTTL()
		minUntil := before.Add(wantTTL / 2)
		maxUntil := after.Add(wantTTL / 2)
		if client.tokenUntil.Before(minUntil) || client.tokenUntil.After(maxUntil) {
			t.Fatalf("tokenUntil = %v, want within [%v, %v] (half of %v)", client.tokenUntil, minUntil, maxUntil, wantTTL)
		}
	})
}

func TestConcurrentTokenCacheMissScrapesOnce(t *testing.T) {
	client := NewCatalogClient(config.CatalogConfig{TokenCacheTTLHours: 12}, slog.Default())
	var mu sync.Mutex
	homeCalls, jsCalls := 0, 0
	client.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		switch req.URL.String() {
		case "https://music.apple.com":
			homeCalls++
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`<script src="/assets/index~abc123.js"></script>`)), Header: make(http.Header)}, nil
		case "https://music.apple.com/assets/index~abc123.js":
			jsCalls++
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`token:"eyJhbGciOiJFUzI1NiJ9.eyJmb28iOiJiYXIifQ.sig-part-here"`)), Header: make(http.Header)}, nil
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("unexpected")), Header: make(http.Header)}, nil
		}
	})}

	const callers = 20
	start := make(chan struct{})
	errCh := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := client.Token(context.Background())
			errCh <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("Token: %v", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if homeCalls != 1 || jsCalls != 1 {
		t.Fatalf("token scrape calls = home %d, js %d; want one each", homeCalls, jsCalls)
	}
}

func TestEnhancedHLSViaWebTokenRetriesOnce401ThenSucceeds(t *testing.T) {
	client := NewCatalogClient(config.CatalogConfig{Language: "en-US", TokenCacheTTLHours: 12}, slog.Default())
	client.webToken = "stale-web-token"
	client.webTokenUntil = time.Now().Add(time.Hour)

	var catalogCalls int
	var authHeaders []string
	client.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Host {
		case "amp-api.music.apple.com":
			catalogCalls++
			authHeaders = append(authHeaders, req.Header.Get("Authorization"))
			if catalogCalls == 1 {
				return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(`{"errors":[{"status":"401"}]}`)), Header: make(http.Header)}, nil
			}
			body := `{"data":[{"id":"song-1","type":"songs","attributes":{"extendedAssetUrls":{"enhancedHls":"https://example.test/master.m3u8"}}}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		case "music.apple.com":
			if req.URL.Path == "/" || req.URL.Path == "" {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`<script src="/assets/index~abc123.js"></script>`)), Header: make(http.Header)}, nil
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`token:"eyJhbGciOiJFUzI1NiJ9.eyJmb28iOiJiYXIifQ.sig-part-here"`)), Header: make(http.Header)}, nil
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("unexpected")), Header: make(http.Header)}, nil
		}
	})}

	master, err := client.EnhancedHLSViaWebToken(context.Background(), "cn", "song-1")
	if err != nil {
		t.Fatalf("EnhancedHLSViaWebToken: %v", err)
	}
	if master != "https://example.test/master.m3u8" {
		t.Fatalf("master = %q", master)
	}
	if catalogCalls != 2 || len(authHeaders) != 2 || authHeaders[0] != "Bearer stale-web-token" || authHeaders[0] == authHeaders[1] {
		t.Fatalf("calls=%d auth=%#v, want stale token then one refreshed retry", catalogCalls, authHeaders)
	}
}

func TestGetWithUserTokenRetriesOnce401ThenSucceeds(t *testing.T) {
	client := NewCatalogClient(config.CatalogConfig{Language: "en-US"}, slog.Default())
	client.token = "stale-token"
	client.tokenUntil = time.Now().Add(time.Hour)

	var catalogCalls int
	var authHeaders []string
	client.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/v1/catalog/cn/songs/song-1" {
			catalogCalls++
			authHeaders = append(authHeaders, req.Header.Get("Authorization"))
			if catalogCalls == 1 {
				return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(`{"errors":[{"status":"401"}]}`)), Header: make(http.Header)}, nil
			}
			body := `{"data":[{"id":"song-1","type":"songs","attributes":{"name":"Song","artistName":"Artist","albumName":"Album"},"relationships":{}}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		}
		if req.URL.String() == "https://music.apple.com" {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`<script src="/assets/index~abc123.js"></script>`)), Header: make(http.Header)}, nil
		}
		if req.URL.String() == "https://music.apple.com/assets/index~abc123.js" {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`token:"eyJhbGciOiJFUzI1NiJ9.eyJmb28iOiJiYXIifQ.sig-part-here"`)), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("unexpected: " + req.URL.String())), Header: make(http.Header)}, nil
	})}

	song, err := client.SongMetadata(context.Background(), "cn", "song-1")
	if err != nil {
		t.Fatalf("expected success after 401 retry, got %v", err)
	}
	if song.ID != "song-1" {
		t.Fatalf("song = %+v", song)
	}
	if catalogCalls != 2 {
		t.Fatalf("catalog calls = %d, want 2 (401 then retry)", catalogCalls)
	}
	if len(authHeaders) != 2 || authHeaders[0] != "Bearer stale-token" || authHeaders[0] == authHeaders[1] {
		t.Fatalf("auth headers = %#v, want stale token then a different re-minted token", authHeaders)
	}
}

func TestGetWithUserTokenReturnsErrorAfterSingleRetryOn401Twice(t *testing.T) {
	client := NewCatalogClient(config.CatalogConfig{Language: "en-US"}, slog.Default())
	client.token = "stale-token"
	client.tokenUntil = time.Now().Add(time.Hour)

	var catalogCalls int
	client.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/v1/catalog/cn/songs/song-1" {
			catalogCalls++
			return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(`{"errors":[{"status":"401"}]}`)), Header: make(http.Header)}, nil
		}
		if req.URL.String() == "https://music.apple.com" {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`<script src="/assets/index~abc123.js"></script>`)), Header: make(http.Header)}, nil
		}
		if req.URL.String() == "https://music.apple.com/assets/index~abc123.js" {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`token:"eyJhbGciOiJFUzI1NiJ9.eyJmb28iOiJiYXIifQ.sig-part-here"`)), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("unexpected: " + req.URL.String())), Header: make(http.Header)}, nil
	})}

	_, err := client.SongMetadata(context.Background(), "cn", "song-1")
	if err == nil {
		t.Fatal("expected error after repeated 401")
	}
	if catalogCalls != 2 {
		t.Fatalf("catalog calls = %d, want 2 (one retry, no infinite loop)", catalogCalls)
	}
}

func TestStationNextTracksRetriesOnce401ThenSucceeds(t *testing.T) {
	client := NewCatalogClient(config.CatalogConfig{Language: "en-US"}, slog.Default())
	client.token = "stale-token"
	client.tokenUntil = time.Now().Add(time.Hour)

	var nextTracksCalls int
	client.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/v1/catalog/us/stations/ra.1":
			body := `{"data":[{"id":"ra.1","type":"stations","attributes":{"name":"My Station","isLive":false,"artwork":{"url":"station-art"},"playParams":{"format":"tracks"}}}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		case "/v1/me/stations/next-tracks/ra.1":
			nextTracksCalls++
			if nextTracksCalls == 1 {
				return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(`{"errors":[{"status":"401"}]}`)), Header: make(http.Header)}, nil
			}
			body := `{"data":[{"id":"song-1","type":"songs","attributes":{"name":"One","artistName":"Artist"}}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		}
		if req.URL.String() == "https://music.apple.com" {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`<script src="/assets/index~abc123.js"></script>`)), Header: make(http.Header)}, nil
		}
		if req.URL.String() == "https://music.apple.com/assets/index~abc123.js" {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`token:"eyJhbGciOiJFUzI1NiJ9.eyJmb28iOiJiYXIifQ.sig-part-here"`)), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("unexpected: " + req.URL.RequestURI())), Header: make(http.Header)}, nil
	})}

	station, err := client.StationTracks(context.Background(), "us", "ra.1", "media-token-xyz")
	if err != nil {
		t.Fatalf("expected success after 401 retry, got %v", err)
	}
	if got, want := trackIDs(station.Tracks), []string{"song-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("track IDs = %#v, want %#v", got, want)
	}
	if nextTracksCalls != 2 {
		t.Fatalf("next-tracks calls = %d, want 2 (401 then retry)", nextTracksCalls)
	}
}

func TestSongMapsHasLyrics(t *testing.T) {
	tests := []struct {
		name                string
		hasLyrics           bool
		hasTimeSyncedLyrics bool
		want                bool
	}{
		{
			name:                "both true",
			hasLyrics:           true,
			hasTimeSyncedLyrics: true,
			want:                true,
		},
		{
			name:                "only hasLyrics true",
			hasLyrics:           true,
			hasTimeSyncedLyrics: false,
			want:                true,
		},
		{
			name:                "only hasTimeSyncedLyrics true",
			hasLyrics:           false,
			hasTimeSyncedLyrics: true,
			want:                true,
		},
		{
			name:                "both false",
			hasLyrics:           false,
			hasTimeSyncedLyrics: false,
			want:                false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewCatalogClient(config.CatalogConfig{Language: "en-US"}, slog.Default())
			client.token = "test-token"
			client.tokenUntil = time.Now().Add(time.Hour)

			var body string
			if tt.hasLyrics && tt.hasTimeSyncedLyrics {
				body = `{"data":[{"id":"123","type":"songs","attributes":{"name":"Song","artistName":"Artist","albumName":"Album","hasLyrics":true,"hasTimeSyncedLyrics":true},"relationships":{}}]}`
			} else if tt.hasLyrics {
				body = `{"data":[{"id":"123","type":"songs","attributes":{"name":"Song","artistName":"Artist","albumName":"Album","hasLyrics":true,"hasTimeSyncedLyrics":false},"relationships":{}}]}`
			} else if tt.hasTimeSyncedLyrics {
				body = `{"data":[{"id":"123","type":"songs","attributes":{"name":"Song","artistName":"Artist","albumName":"Album","hasLyrics":false,"hasTimeSyncedLyrics":true},"relationships":{}}]}`
			} else {
				body = `{"data":[{"id":"123","type":"songs","attributes":{"name":"Song","artistName":"Artist","albumName":"Album","hasLyrics":false,"hasTimeSyncedLyrics":false},"relationships":{}}]}`
			}

			client.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})}

			song, err := client.Song(context.Background(), "cn", "123")
			if err != nil {
				t.Fatal(err)
			}
			if song.HasLyrics != tt.want {
				t.Fatalf("HasLyrics = %v, want %v", song.HasLyrics, tt.want)
			}
		})
	}
}
