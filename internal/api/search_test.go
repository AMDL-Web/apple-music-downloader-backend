package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"amdl/internal/applemusic"
	"amdl/internal/config"
	"amdl/internal/domain"
)

// sampleSearch is a catalog search as applemusic.Search maps Apple's answer:
// the URLs carry the l= query Apple appends, as recorded in
// internal/applemusic/testdata/search-us.json.
func sampleSearch() *applemusic.SearchResults {
	return &applemusic.SearchResults{
		Songs: []applemusic.Song{{
			ID: "6814997425", Name: "Patient Zero", ArtistName: "Taylor Swift", AlbumName: "The Life of a Showgirl: The Encore",
			URL:        "https://music.apple.com/us/album/patient-zero/6814997249?i=6814997425&l=zh-Hans-CN",
			ArtworkURL: "https://is1-ssl.mzstatic.com/a/{w}x{h}bb.jpg", ArtworkColors: palette,
			ReleaseDate: "2026-09-24", GenreNames: []string{"Pop", "Music"}, DurationInMillis: 225868,
			ContentRating: "explicit", AppleDigitalMaster: true, AudioTraits: []string{"atmos", "lossless", "lossy-stereo", "spatial"},
		}},
		Albums: []applemusic.Collection{{
			ID: "6814997249", Type: applemusic.TypeAlbum, Name: "The Life of a Showgirl: The Encore", Artist: "Taylor Swift",
			URL:        "https://music.apple.com/us/album/the-life-of-a-showgirl-the-encore/6814997249?l=zh-Hans-CN",
			ArtworkURL: "https://is1-ssl.mzstatic.com/b/{w}x{h}bb.jpg", ReleaseDate: "2026-09-24", GenreNames: []string{"Pop"},
			TrackCount: 16, AudioTraits: []string{"lossless", "lossy-stereo"},
		}},
		Playlists: []applemusic.Collection{{
			ID: "pl.2553030a768547e9ab3e91230de8439f", Type: applemusic.TypePlaylist, Name: "Lo-fi beats to study, work, sleep and relax to", Artist: "BeatTape",
			URL:        "https://music.apple.com/us/playlist/lo-fi-beats-to-study-work-sleep-and-relax-to/pl.2553030a768547e9ab3e91230de8439f?l=zh-Hans-CN",
			ArtworkURL: "https://is1-ssl.mzstatic.com/c/{w}x{h}bb.jpg", AudioTraits: []string{"lossless"},
		}},
		NextOffset: map[applemusic.SearchType]int{applemusic.SearchSongs: 10},
	}
}

func decodeBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return got
}

func TestSearchReturnsOneGroupPerType(t *testing.T) {
	catalog := &fakeCatalog{t: t, search: sampleSearch()}
	server := newLookupServer(catalog, func(c *config.Config) { c.Catalog.DefaultStorefront = "us" })
	recorder := requestJSON(t, server, http.MethodGet, "/api/v1/search?term=%20Taylor%20%20Swift%20", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	// The term is trimmed with inner runs of space collapsed, the storefront is
	// catalog.default_storefront, and every group is read, 10 per group.
	if want := []string{"search us/Taylor Swift [songs albums playlists artists] 10+0"}; !reflect.DeepEqual(catalog.calls, want) {
		t.Fatalf("catalog calls = %v, want %v", catalog.calls, want)
	}
	want := map[string]any{
		"term": "Taylor Swift", "storefront": "us",
		"songs": map[string]any{
			"items": []any{map[string]any{
				"id": "6814997425", "type": "song", "name": "Patient Zero",
				"url":         "https://music.apple.com/us/song/patient-zero/6814997425",
				"artwork_url": "https://is1-ssl.mzstatic.com/a/{w}x{h}bb.jpg", "artwork_bg_color": "101010",
				"artwork_text_color1": "f1f1f1", "artwork_text_color2": "e2e2e2", "artwork_text_color3": "d3d3d3", "artwork_text_color4": "c4c4c4",
				"artist_name": "Taylor Swift", "album_name": "The Life of a Showgirl: The Encore", "release_date": "2026-09-24",
				"genre": "Pop", "duration_ms": float64(225868), "content_rating": "explicit", "apple_digital_master": true,
				"audio_traits": []any{"atmos", "lossless", "lossy-stereo", "spatial"},
			}},
			"next_offset": float64(10),
		},
		"albums": map[string]any{
			"items": []any{map[string]any{
				"id": "6814997249", "type": "album", "name": "The Life of a Showgirl: The Encore",
				"url":         "https://music.apple.com/us/album/the-life-of-a-showgirl-the-encore/6814997249",
				"artwork_url": "https://is1-ssl.mzstatic.com/b/{w}x{h}bb.jpg",
				"artist_name": "Taylor Swift", "release_date": "2026-09-24", "genre": "Pop", "track_count": float64(16),
				"audio_traits": []any{"lossless", "lossy-stereo"},
			}},
		},
		"playlists": map[string]any{
			"items": []any{map[string]any{
				"id": "pl.2553030a768547e9ab3e91230de8439f", "type": "playlist", "name": "Lo-fi beats to study, work, sleep and relax to",
				"url":         "https://music.apple.com/us/playlist/lo-fi-beats-to-study-work-sleep-and-relax-to/pl.2553030a768547e9ab3e91230de8439f",
				"artwork_url": "https://is1-ssl.mzstatic.com/c/{w}x{h}bb.jpg", "artist_name": "BeatTape",
				"audio_traits": []any{"lossless"},
			}},
		},
		// A group without hits is there, empty.
		"artists": map[string]any{"items": []any{}},
	}
	if got := decodeBody(t, recorder.Body.Bytes()); !reflect.DeepEqual(got, want) {
		t.Fatalf("response = %v\nwant       %v", got, want)
	}
}

func TestSearchReadsOnlyTheRequestedTypes(t *testing.T) {
	catalog := &fakeCatalog{t: t, search: sampleSearch()}
	recorder := requestJSON(t, newLookupServer(catalog, nil), http.MethodGet, "/api/v1/search?term=a&types=albums,songs&types=albums&storefront=JP&limit=50&offset=20", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	if want := []string{"search jp/a [songs albums] 50+20"}; !reflect.DeepEqual(catalog.calls, want) {
		t.Fatalf("catalog calls = %v, want %v", catalog.calls, want)
	}
	got := decodeBody(t, recorder.Body.Bytes())
	if _, ok := got["artists"]; ok {
		t.Fatalf("artists group present though not requested: %v", got)
	}
	if got["storefront"] != "jp" || got["songs"] == nil || got["albums"] == nil {
		t.Fatalf("response = %v", got)
	}
}

func TestSearchReadsPlaylistsBetweenAlbumsAndArtists(t *testing.T) {
	catalog := &fakeCatalog{t: t, search: sampleSearch()}
	recorder := requestJSON(t, newLookupServer(catalog, nil), http.MethodGet, "/api/v1/search?term=a&types=artists,playlists", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	if want := []string{"search cn/a [playlists artists] 10+0"}; !reflect.DeepEqual(catalog.calls, want) {
		t.Fatalf("catalog calls = %v, want %v", catalog.calls, want)
	}
	got := decodeBody(t, recorder.Body.Bytes())
	if got["playlists"] == nil || got["artists"] == nil || got["songs"] != nil || got["albums"] != nil {
		t.Fatalf("response = %v", got)
	}
}

func TestSearchRefusesBadParameters(t *testing.T) {
	for _, query := range []string{
		"",
		"term=%20%20",
		"term=" + strings.Repeat("a", searchMaxTermRunes+1),
		"term=a&types=tracks",
		"term=a&storefront=usa",
		"term=a&limit=0",
		"term=a&limit=51",
		"term=a&limit=x",
		"term=a&offset=-1",
	} {
		catalog := &fakeCatalog{t: t}
		recorder := requestJSON(t, newLookupServer(catalog, nil), http.MethodGet, "/api/v1/search?"+query, "")
		assertErrorCode(t, recorder, http.StatusBadRequest, CodeInvalidRequest)
		if len(catalog.calls) != 0 {
			t.Fatalf("%q read the catalog: %v", query, catalog.calls)
		}
	}
}

func TestSearchCatalogFailures(t *testing.T) {
	for _, c := range []struct {
		err    error
		status int
		code   ErrorCode
	}{
		{errors.New("Apple Music catalog search request in storefront us failed: 400 Bad Request"), http.StatusBadGateway, CodeUpstreamError},
		{context.DeadlineExceeded, http.StatusGatewayTimeout, CodeUpstreamTimeout},
	} {
		recorder := requestJSON(t, newLookupServer(&fakeCatalog{t: t, err: c.err}, nil), http.MethodGet, "/api/v1/search?term=a", "")
		assertErrorCode(t, recorder, c.status, c.code)
	}
}

// A hit a job downloaded, or is downloading, names that job; the job's link
// may be from another storefront. A failed job marks nothing.
func TestSearchMarksHitsWithTheirJobs(t *testing.T) {
	catalog := &fakeCatalog{t: t, search: sampleSearch()}
	server := newServer(t, Deps{Catalog: catalog})
	ctx := context.Background()
	created := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	finished := created.Add(time.Hour)
	for _, job := range []domain.Job{
		{ID: "album-old", Type: "album", CanonicalKey: "album:jp:6814997249", Status: domain.JobCompleted, CreatedAt: created, FinishedAt: &finished},
		{ID: "album-new", Type: "album", CanonicalKey: "album:us:6814997249", Status: domain.JobCompleted, CreatedAt: created, FinishedAt: ptr(finished.Add(time.Hour))},
		{ID: "song-failed", Type: "song", CanonicalKey: "song:us:6814997425", Status: domain.JobFailed, CreatedAt: created},
		{ID: "song-running", Type: "song", CanonicalKey: "song:us:6814997425", Status: domain.JobRunning, CreatedAt: created},
		{ID: "song-done", Type: "song", CanonicalKey: "song:us:6814997425", Status: domain.JobCompleted, CreatedAt: created.Add(time.Hour), FinishedAt: &finished},
		{ID: "playlist-done", Type: "playlist", CanonicalKey: "playlist:us:pl.2553030a768547e9ab3e91230de8439f", Status: domain.JobCompleted, CreatedAt: created, FinishedAt: &finished},
	} {
		job.Input, job.Storefront, job.UpdatedAt = "input", "us", created
		if err := server.store.CreateJob(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	recorder := requestJSON(t, server.Routes(), http.MethodGet, "/api/v1/search?term=a&storefront=us", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	var resp struct {
		Songs, Albums, Playlists struct {
			Items []struct {
				PriorJob *struct {
					ID         string `json:"id"`
					Status     string `json:"status"`
					FinishedAt string `json:"finished_at"`
				} `json:"prior_job"`
			} `json:"items"`
		}
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	// The running job comes before a newer completed one: the song is in the
	// queue now.
	if got := resp.Songs.Items[0].PriorJob; got == nil || got.ID != "song-running" || got.Status != "running" || got.FinishedAt != "" {
		t.Fatalf("song prior job = %+v", got)
	}
	if got := resp.Albums.Items[0].PriorJob; got == nil || got.ID != "album-new" || got.Status != "completed" || got.FinishedAt != "2026-10-01T10:00:00Z" {
		t.Fatalf("album prior job = %+v", got)
	}
	if got := resp.Playlists.Items[0].PriorJob; got == nil || got.ID != "playlist-done" || got.Status != "completed" {
		t.Fatalf("playlist prior job = %+v", got)
	}
}

func ptr[T any](v T) *T { return &v }

// Each hit's url, submitted, downloads exactly that item under either
// catalog.album_track_url_mode.
func TestHitURLsDownloadTheHit(t *testing.T) {
	for _, c := range []struct {
		kind     applemusic.URLType
		appleURL string
		want     string
	}{
		{applemusic.TypeSong, "https://music.apple.com/jp/album/%E6%99%B4%E3%82%8B/1721450220?i=1721450223&l=ja", "https://music.apple.com/jp/song/%E6%99%B4%E3%82%8B/1721450223"},
		{applemusic.TypeSong, "", "https://music.apple.com/jp/song/1721450223"},
		{applemusic.TypeAlbum, "https://music.apple.com/jp/album/bubble-single/1889425900?l=ja", "https://music.apple.com/jp/album/bubble-single/1889425900"},
		{applemusic.TypePlaylist, "https://music.apple.com/jp/playlist/%E5%A4%9C/pl.2553030a768547e9ab3e91230de8439f?l=ja", "https://music.apple.com/jp/playlist/%E5%A4%9C/pl.2553030a768547e9ab3e91230de8439f"},
		{applemusic.TypePlaylist, "", "https://music.apple.com/jp/playlist/pl.2553030a768547e9ab3e91230de8439f"},
		{applemusic.TypeArtist, "https://music.apple.com/jp/artist/%E3%83%A8%E3%83%AB%E3%82%B7%E3%82%AB/1250709916", "https://music.apple.com/jp/artist/%E3%83%A8%E3%83%AB%E3%82%B7%E3%82%AB/1250709916"},
		{applemusic.TypeArtist, "not a url \x7f", "https://music.apple.com/jp/artist/1250709916"},
	} {
		id := map[applemusic.URLType]string{applemusic.TypeSong: "1721450223", applemusic.TypeAlbum: "1889425900", applemusic.TypePlaylist: "pl.2553030a768547e9ab3e91230de8439f", applemusic.TypeArtist: "1250709916"}[c.kind]
		got := hitURL(c.kind, "jp", id, c.appleURL)
		if got != c.want {
			t.Errorf("hitURL(%s, %q) = %q, want %q", c.kind, c.appleURL, got, c.want)
		}
		for _, mode := range []string{"song", "album"} {
			parsed, err := applemusic.ParseWithAlbumTrackMode(got, mode)
			if err != nil || parsed.Type != c.kind || parsed.ID != id || parsed.Storefront != "jp" {
				t.Errorf("%q in mode %s parses as %+v, %v", got, mode, parsed, err)
			}
		}
	}
}

func TestArtistAlbumsReturnsTheArtistAndOnePage(t *testing.T) {
	catalog := &fakeCatalog{t: t,
		artist: &applemusic.Artist{ID: "159260351", Name: "Taylor Swift", URL: "https://music.apple.com/us/artist/taylor-swift/159260351", GenreNames: []string{"Pop"}},
		albums: &applemusic.AlbumPage{Albums: sampleSearch().Albums, NextOffset: 3},
	}
	recorder := requestJSON(t, newLookupServer(catalog, nil), http.MethodGet, "/api/v1/artists/159260351/albums?storefront=us&limit=2&offset=1", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	calls := slices.Clone(catalog.calls)
	slices.Sort(calls)
	if want := []string{"artist albums us/159260351 2+1", "artist us/159260351"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("catalog calls = %v, want %v", calls, want)
	}
	got := decodeBody(t, recorder.Body.Bytes())
	artist, _ := got["artist"].(map[string]any)
	albums, _ := got["albums"].(map[string]any)
	if got["storefront"] != "us" || artist["name"] != "Taylor Swift" || artist["type"] != "artist" || artist["genre"] != "Pop" || albums["next_offset"] != float64(3) {
		t.Fatalf("response = %v", got)
	}
	if items, _ := albums["items"].([]any); len(items) != 1 || items[0].(map[string]any)["track_count"] != float64(16) {
		t.Fatalf("albums = %v", albums)
	}
}

func TestArtistAlbumsRefusesBadParameters(t *testing.T) {
	for _, path := range []string{
		"/api/v1/artists/abc/albums",
		"/api/v1/artists/1%2F2/albums",
		"/api/v1/artists/1/albums?limit=101",
		"/api/v1/artists/1/albums?storefront=x",
	} {
		catalog := &fakeCatalog{t: t}
		assertErrorCode(t, requestJSON(t, newLookupServer(catalog, nil), http.MethodGet, path, ""), http.StatusBadRequest, CodeInvalidRequest)
		if len(catalog.calls) != 0 {
			t.Fatalf("%s read the catalog: %v", path, catalog.calls)
		}
	}
}

func TestArtistAlbumsUnknownArtist(t *testing.T) {
	catalog := &fakeCatalog{t: t, err: notFoundError{}}
	assertErrorCode(t, requestJSON(t, newLookupServer(catalog, nil), http.MethodGet, "/api/v1/artists/1/albums", ""), http.StatusNotFound, CodeCatalogNotFound)
}
