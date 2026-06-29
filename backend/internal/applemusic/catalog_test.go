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

