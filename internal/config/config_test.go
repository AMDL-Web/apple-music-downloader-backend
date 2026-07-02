package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWrapperLoginTimeout(t *testing.T) {
	defaults := Default().Wrapper
	if got := defaults.LoginTimeout(); got != 120*time.Second {
		t.Fatalf("default login timeout = %s, want 2m", got)
	}
	if got := defaults.Timeout(); got != 30*time.Second {
		t.Fatalf("default RPC timeout = %s, want 30s", got)
	}

	configured := WrapperConfig{TimeoutSeconds: 15, LoginTimeoutSeconds: 90}
	if got := configured.LoginTimeout(); got != 90*time.Second {
		t.Fatalf("configured login timeout = %s, want 1m30s", got)
	}
	if got := configured.Timeout(); got != 15*time.Second {
		t.Fatalf("configured RPC timeout = %s, want 15s", got)
	}
}

func TestDefaultLyricsOptions(t *testing.T) {
	defaults := Default().Download
	if defaults.LyricsType != "lyrics" {
		t.Fatalf("default lyrics type = %q, want lyrics", defaults.LyricsType)
	}
	if defaults.LyricsFormat != "lrc" {
		t.Fatalf("default lyrics format = %q, want lrc", defaults.LyricsFormat)
	}
	if len(defaults.LyricsExtras) != 0 {
		t.Fatalf("default lyrics extras = %#v, want empty", defaults.LyricsExtras)
	}
}

func TestDefaultArtistsFolderName(t *testing.T) {
	if got := Default().Download.ArtistsFolderName; got != "artists" {
		t.Fatalf("default artists folder name = %q, want artists", got)
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := writeConfig(t, "download:\n  codec: alac\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "field codec not found") {
		t.Fatalf("Load() error = %v, want unknown field error", err)
	}
}

func TestLoadRejectsExplicitEmptyValues(t *testing.T) {
	path := writeConfig(t, "catalog:\n  album_track_url_mode: \"\"\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "album_track_url_mode") {
		t.Fatalf("Load() error = %v, want album_track_url_mode validation error", err)
	}
}

func TestLoadRejectsUnknownCoverFormat(t *testing.T) {
	path := writeConfig(t, "download:\n  cover_format: webp\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "cover_format") {
		t.Fatalf("Load() error = %v, want cover_format validation error", err)
	}
}

func TestLoadRejectsUnknownLyricsFormat(t *testing.T) {
	path := writeConfig(t, "download:\n  lyrics_format: json\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "lyrics_format") {
		t.Fatalf("Load() error = %v, want lyrics_format validation error", err)
	}
}

func TestLoadRejectsUnknownLyricsType(t *testing.T) {
	path := writeConfig(t, "download:\n  lyrics_type: word-by-word\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "lyrics_type") {
		t.Fatalf("Load() error = %v, want lyrics_type validation error", err)
	}
}

func TestLoadRejectsUnknownLyricsExtra(t *testing.T) {
	path := writeConfig(t, "download:\n  lyrics_extras: [translation, romanization]\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "lyrics_extras") {
		t.Fatalf("Load() error = %v, want lyrics_extras validation error", err)
	}
}

func TestLoadRejectsExplicitAACLCInPriority(t *testing.T) {
	path := writeConfig(t, "download:\n  quality_priority: [alac, aac-lc]\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "aac-lc") {
		t.Fatalf("Load() error = %v, want implicit AAC-LC validation error", err)
	}
}

func TestLoadRejectsEmptyArtistsFolderName(t *testing.T) {
	path := writeConfig(t, "download:\n  artists_folder_name: \"\"\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "artists_folder_name") {
		t.Fatalf("Load() error = %v, want artists_folder_name validation error", err)
	}
}
