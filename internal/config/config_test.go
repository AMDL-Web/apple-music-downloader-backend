package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
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

func TestMemoryModeDefaultLoadAndValidate(t *testing.T) {
	if got := Default().Download.MemoryMode; got != MemoryModeLow {
		t.Fatalf("default memory mode = %q, want %q", got, MemoryModeLow)
	}

	// Configs written before memory_mode existed inherit the low-memory
	// default, preserving the current production behavior.
	cfg, err := loadConfig(t, "download:\n  cover_format: jpg\n")
	if err != nil {
		t.Fatalf("load legacy config: %v", err)
	}
	if cfg.Download.MemoryMode != MemoryModeLow {
		t.Fatalf("legacy config memory mode = %q, want %q", cfg.Download.MemoryMode, MemoryModeLow)
	}

	cfg, err = loadConfig(t, "download:\n  memory_mode: high\n")
	if err != nil {
		t.Fatalf("load high memory mode: %v", err)
	}
	if cfg.Download.MemoryMode != MemoryModeHigh {
		t.Fatalf("loaded memory mode = %q, want %q", cfg.Download.MemoryMode, MemoryModeHigh)
	}

	if _, err := loadConfig(t, "download:\n  memory_mode: auto\n"); err == nil || !strings.Contains(err.Error(), "memory_mode") {
		t.Fatalf("load invalid memory mode error = %v, want memory_mode validation error", err)
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
			if _, err := loadConfig(t, body); err == nil || !strings.Contains(err.Error(), "logging") {
				t.Fatalf("load error = %v, want logging validation error", err)
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
		{name: "progress event interval", apply: func(c *Config, value int) { c.Download.ProgressEventIntervalMS = value }, key: "progress_event_interval_ms", max: maxProgressEventIntervalMSLimit},
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
		// A single song is a collection of one, so only the song template keeps
		// Apple's own track number; the collection templates number by
		// {SongNumber}, which does not restart on every disc.
		"song":     "songs/{ArtistName}/{AlbumName}/{TrackNumber:02d}. {SongName}",
		"album":    "albums/{ArtistName}/{AlbumName}/{SongNumber:02d}. {SongName}",
		"artist":   "artists/{ArtistName}/{AlbumName}/{SongNumber:02d}. {SongName}",
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

// loadConfig resolves a config file body with no database layer and no
// environment overrides — the layer stack startup sees before the database it
// names is open.
func loadConfig(t *testing.T, body string) (Config, error) {
	t.Helper()
	return loadPath(t, writeConfig(t, body))
}

func loadPath(t *testing.T, path string) (Config, error) {
	t.Helper()
	resolver, err := NewResolver(path, nil)
	if err != nil {
		return Config{}, err
	}
	cfg, _, err := resolver.Resolve(nil)
	return cfg, err
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := writeConfig(t, "download:\n  codec: alac\n")
	if _, err := loadPath(t, path); err == nil || !strings.Contains(err.Error(), `unknown configuration key "download.codec"`) {
		t.Fatalf("load error = %v, want unknown field error", err)
	}
}

func TestLoadRejectsRemovedConcurrencyKeys(t *testing.T) {
	for _, key := range []string{"max_parallel_tracks", "max_parallel_metadata_requests", "max_parallel_media_downloads"} {
		t.Run(key, func(t *testing.T) {
			path := writeConfig(t, "download:\n  "+key+": 5\n")
			if _, err := loadPath(t, path); err == nil || !strings.Contains(err.Error(), `unknown configuration key "download.`+key+`"`) {
				t.Fatalf("load error = %v, want removed-field error for %s", err, key)
			}
		})
	}
}

func TestLoadClampsWrapperRequestLimit(t *testing.T) {
	path := writeConfig(t, "download:\n  max_parallel_wrapper_requests: 999\n")
	cfg, err := loadPath(t, path)
	if err != nil {
		t.Fatalf("load oversized wrapper limit: %v", err)
	}
	if cfg.Download.MaxParallelWrapperRequests != maxGlobalPoolLimit {
		t.Fatalf("max_parallel_wrapper_requests = %d, want clamped %d", cfg.Download.MaxParallelWrapperRequests, maxGlobalPoolLimit)
	}
}

func TestLoadRejectsExplicitEmptyValues(t *testing.T) {
	path := writeConfig(t, "catalog:\n  album_track_url_mode: \"\"\n")
	if _, err := loadPath(t, path); err == nil || !strings.Contains(err.Error(), "album_track_url_mode") {
		t.Fatalf("load error = %v, want album_track_url_mode validation error", err)
	}
}

func TestLoadRejectsPartialDeveloperTokenConfig(t *testing.T) {
	path := writeConfig(t, "catalog:\n  apple_music_key_id: \"88KBJL3CKU\"\n")
	if _, err := loadPath(t, path); err == nil || !strings.Contains(err.Error(), "apple_music_") {
		t.Fatalf("load error = %v, want partial signing config error", err)
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

// TestLoadRejectsRemovedMediaUserTokenPriority pins the 2.0 removal of the
// v1.2 compatibility field: it is gone from the struct, so a config file still
// carrying it fails as an unknown key rather than being quietly accepted.
func TestLoadRejectsRemovedMediaUserTokenPriority(t *testing.T) {
	_, err := loadConfig(t, "catalog:\n  media_user_token_priority: request\n")
	if err == nil || !strings.Contains(err.Error(), `unknown configuration key "catalog.media_user_token_priority"`) {
		t.Fatalf("load error = %v, want unknown-key error", err)
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
	if _, err := loadPath(t, path); err == nil || !strings.Contains(err.Error(), "signed_mode_hls_source") {
		t.Fatalf("load error = %v, want signed_mode_hls_source validation error", err)
	}
	path = writeConfig(t, "catalog:\n  signed_mode_hls_source: web_token\n")
	cfg, err := loadPath(t, path)
	if err != nil {
		t.Fatalf("load error = %v", err)
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
	if _, err := loadPath(t, path); err == nil || !strings.Contains(err.Error(), "allowed_origins") {
		t.Fatalf("load error = %v, want allowed_origins validation error", err)
	}
}

func TestLoadAcceptsAllowedOrigins(t *testing.T) {
	path := writeConfig(t, "catalog:\n  allowed_origins: [\"https://example.com\"]\n  developer_token_ttl_hours: 2\n")
	cfg, err := loadPath(t, path)
	if err != nil {
		t.Fatalf("load error = %v", err)
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
	if _, err := loadPath(t, path); err == nil || !strings.Contains(err.Error(), "cover_format") {
		t.Fatalf("load error = %v, want cover_format validation error", err)
	}
}

func TestLoadRejectsUnknownLyricsFormat(t *testing.T) {
	path := writeConfig(t, "download:\n  lyrics_format: json\n")
	if _, err := loadPath(t, path); err == nil || !strings.Contains(err.Error(), "lyrics_format") {
		t.Fatalf("load error = %v, want lyrics_format validation error", err)
	}
}

func TestLoadRejectsUnknownLyricsType(t *testing.T) {
	path := writeConfig(t, "download:\n  lyrics_type: word-by-word\n")
	if _, err := loadPath(t, path); err == nil || !strings.Contains(err.Error(), "lyrics_type") {
		t.Fatalf("load error = %v, want lyrics_type validation error", err)
	}
}

func TestLoadRejectsUnknownLyricsExtra(t *testing.T) {
	path := writeConfig(t, "download:\n  lyrics_extras: [translation, romanization]\n")
	if _, err := loadPath(t, path); err == nil || !strings.Contains(err.Error(), "lyrics_extras") {
		t.Fatalf("load error = %v, want lyrics_extras validation error", err)
	}
}

func TestLoadRejectsExplicitAACLCInPriority(t *testing.T) {
	path := writeConfig(t, "download:\n  quality_priority: [alac, aac-lc]\n")
	if _, err := loadPath(t, path); err == nil || !strings.Contains(err.Error(), "aac-lc") {
		t.Fatalf("load error = %v, want implicit AAC-LC validation error", err)
	}
}

func TestDefaultConfigPassesValidation(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("Default().Validate() error = %v", err)
	}
}

func TestLoadRejectsEmptyPathFormat(t *testing.T) {
	path := writeConfig(t, "download:\n  artist_path_format: \"\"\n")
	if _, err := loadPath(t, path); err == nil || !strings.Contains(err.Error(), "artist_path_format") {
		t.Fatalf("load error = %v, want artist_path_format validation error", err)
	}
}

// TestLoadClampsResourceLimitsFromFile covers machine-managed config files
// that may hold values above limits introduced by a newer backend.
func TestLoadClampsResourceLimitsFromFile(t *testing.T) {
	path := writeConfig(t, "catalog:\n  max_parallel_requests: 200\n  requests_per_second: 200\n  request_burst: 200\ndownload:\n  max_running_jobs: 100\n  max_parallel_downloads: 200\n  max_parallel_decrypts: 200\n  max_attempts: 50\n")
	cfg, err := loadPath(t, path)
	if err != nil {
		t.Fatalf("load with over-limit values failed: %v", err)
	}
	if cfg.Catalog.MaxParallelRequests != maxGlobalPoolLimit || cfg.Catalog.RequestsPerSecond != maxGlobalPoolLimit || cfg.Catalog.RequestBurst != maxGlobalPoolLimit {
		t.Fatalf("load did not clamp catalog values: %+v", cfg.Catalog)
	}
	if cfg.Download.MaxRunningJobs != maxRunningJobsLimit || cfg.Download.MaxParallelDownloads != maxGlobalPoolLimit || cfg.Download.MaxParallelDecrypts != maxGlobalPoolLimit || cfg.Download.MaxAttempts != maxAttemptsLimit {
		t.Fatalf("load did not clamp download values: %+v", cfg.Download)
	}
}

// committedConfig splits the tracked configs/config.example.yaml into the keys
// it writes out and the keys it documents but leaves commented, and returns a
// copy with every commented key activated. A commented line counts as a key
// only when the name before the colon is a real config key, which is what
// separates "# level: info" from the prose above it.
func committedConfig(t *testing.T) (active, commented map[string]bool, uncommented []byte) {
	t.Helper()
	raw, err := os.ReadFile("../../configs/config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	known := knownKeys()
	sectionPattern := regexp.MustCompile(`^([a-z_]+):\s*$`)
	activePattern := regexp.MustCompile(`^  ([a-z_0-9]+):`)
	commentedPattern := regexp.MustCompile(`^  # ([a-z_0-9]+):`)

	active, commented = map[string]bool{}, map[string]bool{}
	var section string
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		if m := sectionPattern.FindStringSubmatch(line); m != nil {
			section = m[1]
			out = append(out, line)
			continue
		}
		if m := activePattern.FindStringSubmatch(line); m != nil && section != "" {
			if key := section + "." + m[1]; known[key] {
				if active[key] {
					t.Fatalf("configs/config.example.yaml sets %s twice", key)
				}
				active[key] = true
			}
			out = append(out, line)
			continue
		}
		if m := commentedPattern.FindStringSubmatch(line); m != nil && section != "" {
			if key := section + "." + m[1]; known[key] {
				if commented[key] {
					t.Fatalf("configs/config.example.yaml documents %s twice", key)
				}
				commented[key] = true
				out = append(out, "  "+strings.TrimPrefix(line[2:], "# "))
				continue
			}
		}
		out = append(out, line)
	}
	return active, commented, []byte(strings.Join(out, "\n"))
}

// TestCommittedConfigDocumentsEveryKey keeps the shipped file in step with the
// struct: every key appears exactly once, active or commented, and nothing
// appears that is not a key.
func TestCommittedConfigDocumentsEveryKey(t *testing.T) {
	active, commented, _ := committedConfig(t)
	var missing []string
	for key := range knownKeys() {
		if !active[key] && !commented[key] {
			missing = append(missing, key)
		}
	}
	slices.Sort(missing)
	if len(missing) > 0 {
		t.Fatalf("configs/config.example.yaml documents neither an active nor a commented entry for: %v", missing)
	}
}

// TestCommittedConfigLeavesRuntimeKeysToTheAPI pins the split a fresh install
// ships with: startup-bound keys are written out, because the file and the
// environment are the only layers that can set them, while every
// runtime-mutable key stays commented so the database layer owns it. An active
// runtime key would pin itself and take the setting away from the API on every
// new install.
func TestCommittedConfigLeavesRuntimeKeysToTheAPI(t *testing.T) {
	active, commented, _ := committedConfig(t)
	var pinned, orphaned []string
	for key := range knownKeys() {
		if isRuntimeKey(key) && active[key] {
			pinned = append(pinned, key)
		}
		if !isRuntimeKey(key) && commented[key] {
			orphaned = append(orphaned, key)
		}
	}
	slices.Sort(pinned)
	slices.Sort(orphaned)
	if len(pinned) > 0 {
		t.Fatalf("configs/config.example.yaml activates runtime-mutable keys, pinning them away from the API: %v", pinned)
	}
	if len(orphaned) > 0 {
		t.Fatalf("configs/config.example.yaml comments out startup-bound keys, which no other layer can set: %v", orphaned)
	}
}

// TestCommittedConfigMatchesDefaults is the single check that keeps the Go
// defaults and the shipped file from drifting: activating every documented key
// must resolve to exactly Default(), so what the file shows is what a config
// omitting it gets.
func TestCommittedConfigMatchesDefaults(t *testing.T) {
	_, _, uncommented := committedConfig(t)
	cfg, err := loadConfig(t, string(uncommented))
	if err != nil {
		t.Fatalf("configs/config.example.yaml does not load with every key active: %v", err)
	}
	if diff := configDiff(cfg, Default()); len(diff) > 0 {
		t.Fatalf("configs/config.example.yaml and Default() disagree on: %s", strings.Join(diff, ", "))
	}
}

// configDiff names the keys whose values differ, which reads far better than
// dumping two whole structs.
func configDiff(got, want Config) []string {
	var diff []string
	gotValue, wantValue := reflect.ValueOf(got), reflect.ValueOf(want)
	for _, field := range envFields() {
		g := gotValue.FieldByIndex(field.index).Interface()
		w := wantValue.FieldByIndex(field.index).Interface()
		if !reflect.DeepEqual(g, w) {
			diff = append(diff, fmt.Sprintf("%s (file %#v, default %#v)", field.key, g, w))
		}
	}
	slices.Sort(diff)
	return diff
}

// TestSeededConfigLoadsAndPinsNothing covers the file a fresh install actually
// gets. EnsureFile copies the example verbatim, so the copy is only safe
// because the example pins nothing the API owns — that is what makes seeding a
// config file compatible with the API owning the runtime keys.
func TestSeededConfigLoadsAndPinsNothing(t *testing.T) {
	raw, err := os.ReadFile("../../configs/config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, configExampleFileName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	created, err := EnsureFile(path)
	if err != nil || !created {
		t.Fatalf("EnsureFile() = (%v, %v), want a created config", created, err)
	}
	seeded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(seeded) != string(raw) {
		t.Fatal("seeded config is not a verbatim copy of the example")
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("seeded config mode = %v, want owner-only", info.Mode().Perm())
	}

	resolver, err := NewResolver(path, nil)
	if err != nil {
		t.Fatalf("seeded config does not read: %v", err)
	}
	cfg, sources, err := resolver.Resolve(nil)
	if err != nil {
		t.Fatalf("seeded config does not load: %v", err)
	}
	if diff := configDiff(cfg, Default()); len(diff) > 0 {
		t.Fatalf("seeded config resolves away from the defaults: %s", strings.Join(diff, ", "))
	}
	if locked := LockedView(sources); len(locked) > 0 {
		t.Fatalf("seeded config pins runtime keys, taking them away from the API on every fresh install: %v", locked)
	}

	// Seeding runs on every start and must never overwrite the operator's file.
	if err := os.WriteFile(path, []byte("logging:\n  format: json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err = EnsureFile(path)
	if err != nil || created {
		t.Fatalf("EnsureFile() over an existing config = (%v, %v), want a no-op", created, err)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "logging:\n  format: json\n" {
		t.Fatalf("EnsureFile() overwrote the existing config: %q (%v)", raw, err)
	}
}

// TestEnsureFileWithoutExample: both files are optional, so an install with
// neither starts on stored settings and defaults instead of refusing to boot.
func TestEnsureFileWithoutExample(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	created, err := EnsureFile(path)
	if err != nil || created {
		t.Fatalf("EnsureFile() with no example = (%v, %v), want a quiet no-op", created, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("EnsureFile() created a config without an example to copy: %v", err)
	}
}

// TestEnsureFileRejectsBrokenExample keeps a bad template from being planted as
// the operator's own file.
func TestEnsureFileRejectsBrokenExample(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, configExampleFileName), []byte("download:\n  nope: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	if _, err := EnsureFile(path); err == nil || !strings.Contains(err.Error(), "download.nope") {
		t.Fatalf("EnsureFile() with a broken example = %v, want an error naming the key", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("EnsureFile() planted a copy of a broken example")
	}
}
