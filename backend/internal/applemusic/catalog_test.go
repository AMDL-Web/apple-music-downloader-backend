package applemusic

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"amdl/backend/internal/config"
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

