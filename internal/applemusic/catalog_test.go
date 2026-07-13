package applemusic

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
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

	playlist, err := client.Playlist(context.Background(), "cn", "pl.1")
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
