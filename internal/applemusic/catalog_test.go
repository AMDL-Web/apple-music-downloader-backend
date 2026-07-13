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
