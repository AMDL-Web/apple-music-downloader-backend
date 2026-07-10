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

func TestLoadRejectsPartialDeveloperTokenConfig(t *testing.T) {
	path := writeConfig(t, "catalog:\n  apple_music_key_id: \"88KBJL3CKU\"\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "apple_music_") {
		t.Fatalf("Load() error = %v, want partial signing config error", err)
	}
}

func TestDeveloperTokenSigningEnabled(t *testing.T) {
	if Default().Catalog.DeveloperTokenSigningEnabled() {
		t.Fatal("default config should have signing disabled")
	}
	complete := CatalogConfig{
		AppleMusicPrivateKeyPath: "keys/AuthKey.p8",
		AppleMusicKeyID:          "88KBJL3CKU",
		AppleMusicTeamID:         "2VTXNMR2GL",
	}
	if !complete.DeveloperTokenSigningEnabled() {
		t.Fatal("complete config should have signing enabled")
	}
}

func TestDeveloperTokenTTL(t *testing.T) {
	if got := Default().Catalog.DeveloperTokenTTL(); got != time.Hour {
		t.Fatalf("default developer token TTL = %s, want 1h", got)
	}
	if got := (CatalogConfig{DeveloperTokenTTLHours: 0}).DeveloperTokenTTL(); got != time.Hour {
		t.Fatalf("zero-value developer token TTL = %s, want 1h fallback", got)
	}
	if got := (CatalogConfig{DeveloperTokenTTLHours: 6}).DeveloperTokenTTL(); got != 6*time.Hour {
		t.Fatalf("configured developer token TTL = %s, want 6h", got)
	}
}

func TestLoadRejectsBlankAllowedOrigin(t *testing.T) {
	path := writeConfig(t, "catalog:\n  allowed_origins: [\"https://example.com\", \"  \"]\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "allowed_origins") {
		t.Fatalf("Load() error = %v, want allowed_origins validation error", err)
	}
}

func TestLoadAcceptsAllowedOrigins(t *testing.T) {
	path := writeConfig(t, "catalog:\n  allowed_origins: [\"https://example.com\"]\n  developer_token_ttl_hours: 2\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Catalog.AllowedOrigins) != 1 || cfg.Catalog.AllowedOrigins[0] != "https://example.com" {
		t.Fatalf("allowed origins = %#v", cfg.Catalog.AllowedOrigins)
	}
	if got := cfg.Catalog.DeveloperTokenTTL(); got != 2*time.Hour {
		t.Fatalf("developer token TTL = %s, want 2h", got)
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

func TestLoadRejectsRemovedSongNumerMisspelling(t *testing.T) {
	for name, body := range map[string]string{
		"playlist_song_file_format padded": "download:\n  playlist_song_file_format: \"{SongNumer:02d}. {SongName}\"\n",
		"song_file_format bare":            "download:\n  song_file_format: \"{SongNumer}. {SongName}\"\n",
	} {
		path := writeConfig(t, body)
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "{SongNumber}") {
			t.Fatalf("Load(%s) error = %v, want {SongNumer} rejection pointing to {SongNumber}", name, err)
		}
	}
}

func TestDefaultConfigPassesValidation(t *testing.T) {
	if err := Default().validate(); err != nil {
		t.Fatalf("Default().validate() error = %v", err)
	}
}

func TestLoadRejectsEmptyArtistsFolderName(t *testing.T) {
	path := writeConfig(t, "download:\n  artists_folder_name: \"\"\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "artists_folder_name") {
		t.Fatalf("Load() error = %v, want artists_folder_name validation error", err)
	}
}
