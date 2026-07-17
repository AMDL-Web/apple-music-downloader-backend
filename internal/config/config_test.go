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

func TestDefaultLogging(t *testing.T) {
	logging := Default().Logging
	if logging.Level != "info" || logging.Format != "text" || !logging.Console || logging.AccessLog {
		t.Fatalf("default logging = %+v", logging)
	}
	if logging.FileEnabled || logging.BufferSize != 2000 {
		t.Fatalf("default logging outputs = %+v", logging)
	}
}

func TestDefaultGlobalConcurrencyControls(t *testing.T) {
	cfg := Default()
	if cfg.Catalog.MaxParallelRequests != 16 || cfg.Catalog.RequestsPerSecond != 10 || cfg.Catalog.RequestBurst != 16 {
		t.Fatalf("default catalog controls = %+v", cfg.Catalog)
	}
	if cfg.Download.MaxParallelDownloads != 16 || cfg.Download.MaxParallelDecrypts != 32 || cfg.Download.MaxParallelWrapperRequests != 24 {
		t.Fatalf("default media pools = %+v", cfg.Download)
	}
}

func TestLoadValidatesLogging(t *testing.T) {
	for name, body := range map[string]string{
		"level":        "logging:\n  level: trace\n",
		"format":       "logging:\n  format: xml\n",
		"buffer":       "logging:\n  buffer_size: -1\n",
		"file path":    "logging:\n  file_enabled: true\n  file_path: \"\"\n",
		"max size":     "logging:\n  max_size_mb: 0\n",
		"all disabled": "logging:\n  console: false\n  file_enabled: false\n  buffer_size: 0\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, body)); err == nil || !strings.Contains(err.Error(), "logging") {
				t.Fatalf("Load() error = %v, want logging validation error", err)
			}
		})
	}
}

func TestValidateBoundsResourceAmplifyingDownloadSettings(t *testing.T) {
	tests := []struct {
		name  string
		apply func(*Config, int)
		key   string
		max   int
	}{
		{name: "running jobs", apply: func(c *Config, value int) { c.Download.MaxRunningJobs = value }, key: "max_running_jobs", max: maxRunningJobsLimit},
		{name: "parallel downloads", apply: func(c *Config, value int) { c.Download.MaxParallelDownloads = value }, key: "max_parallel_downloads", max: maxGlobalPoolLimit},
		{name: "parallel decrypts", apply: func(c *Config, value int) { c.Download.MaxParallelDecrypts = value }, key: "max_parallel_decrypts", max: maxGlobalPoolLimit},
		{name: "catalog parallel requests", apply: func(c *Config, value int) { c.Catalog.MaxParallelRequests = value }, key: "max_parallel_requests", max: maxGlobalPoolLimit},
		{name: "catalog requests per second", apply: func(c *Config, value int) { c.Catalog.RequestsPerSecond = value }, key: "requests_per_second", max: maxGlobalPoolLimit},
		{name: "catalog request burst", apply: func(c *Config, value int) { c.Catalog.RequestBurst = value }, key: "request_burst", max: maxGlobalPoolLimit},
		{name: "attempts", apply: func(c *Config, value int) { c.Download.MaxAttempts = value }, key: "max_attempts", max: maxAttemptsLimit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, value := range []int{tt.max + 1} {
				cfg := Default()
				tt.apply(&cfg, value)
				if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), tt.key) {
					t.Fatalf("Validate() with %s=%d error = %v, want %s bounds error", tt.key, value, err, tt.key)
				}
			}
			// Preserve the established compatibility contract: non-positive
			// values are normalized to one by their consumers.
			for _, value := range []int{-1, 0, 1, tt.max} {
				cfg := Default()
				tt.apply(&cfg, value)
				if err := cfg.Validate(); err != nil {
					t.Fatalf("Validate() rejected boundary %s=%d: %v", tt.key, value, err)
				}
			}
		})
	}
}

func TestDefaultPathFormats(t *testing.T) {
	defaults := Default().Download
	want := map[string]string{
		"song":     "songs/{ArtistName}/{AlbumName}/{TrackNumber:02d}. {SongName}",
		"album":    "albums/{ArtistName}/{AlbumName}/{TrackNumber:02d}. {SongName}",
		"artist":   "artists/{ArtistName}/{AlbumName}/{TrackNumber:02d}. {SongName}",
		"playlist": "playlists/{PlaylistName}/{SongNumber:02d}. {SongName}",
	}
	got := map[string]string{
		"song":     defaults.SongPathFormat,
		"album":    defaults.AlbumPathFormat,
		"artist":   defaults.ArtistPathFormat,
		"playlist": defaults.PlaylistPathFormat,
	}
	for kind, wantFormat := range want {
		if got[kind] != wantFormat {
			t.Fatalf("default %s path format = %q, want %q", kind, got[kind], wantFormat)
		}
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

func TestLoadRejectsRemovedConcurrencyKeys(t *testing.T) {
	for _, key := range []string{"max_parallel_tracks", "max_parallel_metadata_requests", "max_parallel_media_downloads"} {
		t.Run(key, func(t *testing.T) {
			path := writeConfig(t, "download:\n  "+key+": 5\n")
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "field "+key+" not found") {
				t.Fatalf("Load() error = %v, want removed-field error for %s", err, key)
			}
		})
	}
}

func TestLoadClampsWrapperRequestLimit(t *testing.T) {
	path := writeConfig(t, "download:\n  max_parallel_wrapper_requests: 999\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() oversized wrapper limit: %v", err)
	}
	if cfg.Download.MaxParallelWrapperRequests != maxGlobalPoolLimit {
		t.Fatalf("max_parallel_wrapper_requests = %d, want clamped %d", cfg.Download.MaxParallelWrapperRequests, maxGlobalPoolLimit)
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

func TestLoadMigratesLegacyMediaUserTokenPriority(t *testing.T) {
	path := writeConfig(t, "catalog:\n  media_user_token: configured-token\n  media_user_token_priority: request\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() legacy priority: %v", err)
	}
	if cfg.Catalog.MediaUserToken != "configured-token" {
		t.Fatalf("media_user_token = %q, want configured-token", cfg.Catalog.MediaUserToken)
	}
	if cfg.Catalog.LegacyMediaUserTokenPriority != "" {
		t.Fatalf("legacy priority survived normalization: %q", cfg.Catalog.LegacyMediaUserTokenPriority)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save() normalized config: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "media_user_token_priority") {
		t.Fatalf("managed-file rewrite kept deprecated priority:\n%s", raw)
	}
}

func TestLoadRejectsUnknownMediaUserTokenPriority(t *testing.T) {
	path := writeConfig(t, "catalog:\n  media_user_token_priority: always\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "media_user_token_priority") {
		t.Fatalf("Load() error = %v, want media_user_token_priority validation error", err)
	}
}

func TestSignedModeHLSSourceDefaultAndValidate(t *testing.T) {
	if got := Default().Catalog.SignedModeHLSSource; got != "wrapper" {
		t.Fatalf("default signed_mode_hls_source = %q, want wrapper", got)
	}
	if Default().Catalog.EnhancedHLSFromWebToken() {
		t.Fatal("default should not use web-token HLS source")
	}
	web := CatalogConfig{SignedModeHLSSource: "web_token"}
	if !web.EnhancedHLSFromWebToken() {
		t.Fatal("web_token should enable EnhancedHLSFromWebToken")
	}
	path := writeConfig(t, "catalog:\n  signed_mode_hls_source: device\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "signed_mode_hls_source") {
		t.Fatalf("Load() error = %v, want signed_mode_hls_source validation error", err)
	}
	path = writeConfig(t, "catalog:\n  signed_mode_hls_source: web_token\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Catalog.SignedModeHLSSource != "web_token" {
		t.Fatalf("signed_mode_hls_source = %q, want web_token", cfg.Catalog.SignedModeHLSSource)
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

func TestDefaultConfigPassesValidation(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("Default().Validate() error = %v", err)
	}
}

func TestLoadRejectsEmptyPathFormat(t *testing.T) {
	path := writeConfig(t, "download:\n  artist_path_format: \"\"\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "artist_path_format") {
		t.Fatalf("Load() error = %v, want artist_path_format validation error", err)
	}
}

// TestLoadClampsResourceLimitsFromFile covers machine-managed config files
// that may hold values above limits introduced by a newer backend.
func TestLoadClampsResourceLimitsFromFile(t *testing.T) {
	path := writeConfig(t, "catalog:\n  max_parallel_requests: 200\n  requests_per_second: 200\n  request_burst: 200\ndownload:\n  max_running_jobs: 100\n  max_parallel_downloads: 200\n  max_parallel_decrypts: 200\n  max_attempts: 50\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() with over-limit values failed: %v", err)
	}
	if cfg.Catalog.MaxParallelRequests != maxGlobalPoolLimit || cfg.Catalog.RequestsPerSecond != maxGlobalPoolLimit || cfg.Catalog.RequestBurst != maxGlobalPoolLimit {
		t.Fatalf("Load() did not clamp catalog values: %+v", cfg.Catalog)
	}
	if cfg.Download.MaxRunningJobs != maxRunningJobsLimit || cfg.Download.MaxParallelDownloads != maxGlobalPoolLimit || cfg.Download.MaxParallelDecrypts != maxGlobalPoolLimit || cfg.Download.MaxAttempts != maxAttemptsLimit {
		t.Fatalf("Load() did not clamp download values: %+v", cfg.Download)
	}
}
