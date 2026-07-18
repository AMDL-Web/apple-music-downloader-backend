package config

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   ServerConfig   `yaml:"server" json:"server"`
	Database DatabaseConfig `yaml:"database" json:"database"`
	Logging  LoggingConfig  `yaml:"logging" json:"logging"`
	Wrapper  WrapperConfig  `yaml:"wrapper" json:"wrapper"`
	Catalog  CatalogConfig  `yaml:"catalog" json:"catalog"`
	Download DownloadConfig `yaml:"download" json:"download"`
	Tools    ToolsConfig    `yaml:"tools" json:"tools"`
	Simulate SimulateConfig `yaml:"simulate" json:"simulate"`
}

type ServerConfig struct {
	Listen string `yaml:"listen" json:"listen"`
}

type DatabaseConfig struct {
	Path string `yaml:"path" json:"path"`
}

// LoggingConfig controls the process-wide structured logger. The whole
// output shape is startup-bound because changing files and handler structure
// while requests are in flight would make persistence failures ambiguous;
// Level and AccessLog are safe to update at runtime.
type LoggingConfig struct {
	Level         string `yaml:"level" json:"level"`
	Format        string `yaml:"format" json:"format"`
	Console       bool   `yaml:"console" json:"console"`
	IncludeSource bool   `yaml:"include_source" json:"include_source"`
	AccessLog     bool   `yaml:"access_log" json:"access_log"`
	BufferSize    int    `yaml:"buffer_size" json:"buffer_size"`
	FileEnabled   bool   `yaml:"file_enabled" json:"file_enabled"`
	FilePath      string `yaml:"file_path" json:"file_path"`
	MaxSizeMB     int    `yaml:"max_size_mb" json:"max_size_mb"`
	MaxBackups    int    `yaml:"max_backups" json:"max_backups"`
	MaxAgeDays    int    `yaml:"max_age_days" json:"max_age_days"`
	Compress      bool   `yaml:"compress" json:"compress"`
}

type WrapperConfig struct {
	Address             string `yaml:"address" json:"address"`
	Insecure            bool   `yaml:"insecure" json:"insecure"`
	TimeoutSeconds      int    `yaml:"timeout_seconds" json:"timeout_seconds"`
	LoginTimeoutSeconds int    `yaml:"login_timeout_seconds" json:"login_timeout_seconds"`
}

func (c WrapperConfig) LoginTimeout() time.Duration {
	if c.LoginTimeoutSeconds <= 0 {
		return 120 * time.Second
	}
	return time.Duration(c.LoginTimeoutSeconds) * time.Second
}

func (c WrapperConfig) Timeout() time.Duration {
	if c.TimeoutSeconds <= 0 {
		return 30 * time.Second
	}
	return time.Duration(c.TimeoutSeconds) * time.Second
}

type CatalogConfig struct {
	DefaultStorefront        string   `yaml:"default_storefront" json:"default_storefront"`
	Language                 string   `yaml:"language" json:"language"`
	MaxParallelRequests      int      `yaml:"max_parallel_requests" json:"max_parallel_requests"`
	RequestsPerSecond        int      `yaml:"requests_per_second" json:"requests_per_second"`
	RequestBurst             int      `yaml:"request_burst" json:"request_burst"`
	AppleMusicPrivateKeyPath string   `yaml:"apple_music_private_key_path" json:"apple_music_private_key_path"`
	AppleMusicKeyID          string   `yaml:"apple_music_key_id" json:"apple_music_key_id"`
	AppleMusicTeamID         string   `yaml:"apple_music_team_id" json:"apple_music_team_id"`
	DeveloperTokenTTLHours   int      `yaml:"developer_token_ttl_hours" json:"developer_token_ttl_hours"`
	AllowedOrigins           []string `yaml:"allowed_origins" json:"allowed_origins"`
	TokenCacheTTLHours       int      `yaml:"token_cache_ttl_hours" json:"token_cache_ttl_hours"`
	AlbumTrackURLMode        string   `yaml:"album_track_url_mode" json:"album_track_url_mode"`
	MediaUserToken           string   `yaml:"media_user_token" json:"media_user_token"`
	// LegacyMediaUserTokenPriority keeps v1.2 config files, environment
	// variables, and config API payloads readable during migration. Request
	// tokens now use the ordinary per-job override semantics, so this value is
	// validated and discarded by Config.NormalizeDeprecated.
	LegacyMediaUserTokenPriority string `yaml:"media_user_token_priority,omitempty" json:"media_user_token_priority,omitempty"`
	SignedModeHLSSource          string `yaml:"signed_mode_hls_source" json:"signed_mode_hls_source"`
}

// DeveloperTokenTTL returns the validity of developer tokens minted for
// external clients via GET /api/v1/developer-token. Values <= 0 fall back to
// 1 hour. The backend's internal token keeps its own fixed 24h validity.
func (c CatalogConfig) DeveloperTokenTTL() time.Duration {
	if c.DeveloperTokenTTLHours <= 0 {
		return time.Hour
	}
	return time.Duration(c.DeveloperTokenTTLHours) * time.Hour
}

func (c CatalogConfig) TokenTTL() time.Duration {
	if c.TokenCacheTTLHours <= 0 {
		return 12 * time.Hour
	}
	return time.Duration(c.TokenCacheTTLHours) * time.Hour
}

// DeveloperTokenSigningEnabled reports whether all three Apple Music
// developer-token signing fields are configured. Partial configuration is
// rejected by validate(), so a true result implies a usable signing setup.
func (c CatalogConfig) DeveloperTokenSigningEnabled() bool {
	return strings.TrimSpace(c.AppleMusicPrivateKeyPath) != "" &&
		strings.TrimSpace(c.AppleMusicKeyID) != "" &&
		strings.TrimSpace(c.AppleMusicTeamID) != ""
}

// EnhancedHLSFromWebToken reports whether, in signed developer-token mode, the
// Enhanced HLS master playlist should be read via a scraped music.apple.com
// web-player token instead of the wrapper's authorized device manifest.
// Meaningless (and ignored) when signing is disabled.
func (c CatalogConfig) EnhancedHLSFromWebToken() bool {
	return c.SignedModeHLSSource == "web_token"
}

type DownloadConfig struct {
	QualityPriority            []string `yaml:"quality_priority" json:"quality_priority"`
	CodecAlternative           bool     `yaml:"codec_alternative" json:"codec_alternative"`
	MemoryMode                 string   `yaml:"memory_mode" json:"memory_mode"`
	MaxRunningJobs             int      `yaml:"max_running_jobs" json:"max_running_jobs"`
	MaxParallelDownloads       int      `yaml:"max_parallel_downloads" json:"max_parallel_downloads"`
	MaxParallelDecrypts        int      `yaml:"max_parallel_decrypts" json:"max_parallel_decrypts"`
	MaxParallelWrapperRequests int      `yaml:"max_parallel_wrapper_requests" json:"max_parallel_wrapper_requests"`
	MaxAttempts                int      `yaml:"max_attempts" json:"max_attempts"`
	DownloadsDir               string   `yaml:"downloads_dir" json:"downloads_dir"`
	SongPathFormat             string   `yaml:"song_path_format" json:"song_path_format"`
	AlbumPathFormat            string   `yaml:"album_path_format" json:"album_path_format"`
	ArtistPathFormat           string   `yaml:"artist_path_format" json:"artist_path_format"`
	PlaylistPathFormat         string   `yaml:"playlist_path_format" json:"playlist_path_format"`
	StationPathFormat          string   `yaml:"station_path_format" json:"station_path_format"`
	TempDir                    string   `yaml:"temp_dir" json:"temp_dir"`
	CoverSize                  string   `yaml:"cover_size" json:"cover_size"`
	CoverFormat                string   `yaml:"cover_format" json:"cover_format"`
	EmbedCover                 bool     `yaml:"embed_cover" json:"embed_cover"`
	SaveAlbumCover             bool     `yaml:"save_album_cover" json:"save_album_cover"`
	SaveArtistCover            bool     `yaml:"save_artist_cover" json:"save_artist_cover"`
	SavePlaylistCover          bool     `yaml:"save_playlist_cover" json:"save_playlist_cover"`
	EmbedLyrics                bool     `yaml:"embed_lyrics" json:"embed_lyrics"`
	SaveLyricsFile             bool     `yaml:"save_lyrics_file" json:"save_lyrics_file"`
	LyricsFormat               string   `yaml:"lyrics_format" json:"lyrics_format"`
	LyricsType                 string   `yaml:"lyrics_type" json:"lyrics_type"`
	LyricsExtras               []string `yaml:"lyrics_extras" json:"lyrics_extras"`
	ALACMaxSampleRate          int      `yaml:"alac_max_sample_rate" json:"alac_max_sample_rate"`
	ALACMaxBitDepth            int      `yaml:"alac_max_bit_depth" json:"alac_max_bit_depth"`
	CheckIntegrity             bool     `yaml:"check_integrity" json:"check_integrity"`
}

const (
	MemoryModeLow  = "low"
	MemoryModeHigh = "high"

	// These limits bound process-wide concurrency, request rate, and retry
	// fan-out while remaining comfortably above normal deployments.
	maxRunningJobsLimit = 32
	maxGlobalPoolLimit  = 64
	maxAttemptsLimit    = 10
)

type ToolsConfig struct {
	FFmpeg string `yaml:"ffmpeg" json:"ffmpeg"`
}

// SimulateConfig drives the local test mode: when enabled, download jobs run
// through the exact same status/progress/event lifecycle as a real download,
// but media selection, transfer, decryption, and disk writes are simulated.
// Transfer progress advances at a random speed drawn from
// [MinSpeedKBps, MaxSpeedKBps] kilobytes per second.
type SimulateConfig struct {
	Enabled      bool `yaml:"enabled" json:"enabled"`
	MinSpeedKBps int  `yaml:"min_speed_kbps" json:"min_speed_kbps"`
	MaxSpeedKBps int  `yaml:"max_speed_kbps" json:"max_speed_kbps"`
}

func Default() Config {
	return Config{
		Server:   ServerConfig{Listen: "127.0.0.1:18080"},
		Database: DatabaseConfig{Path: "data/db/amdl.db"},
		Logging: LoggingConfig{
			Level: "info", Format: "text", Console: true, AccessLog: false,
			BufferSize: 2000, FilePath: "data/logs/amdl.log", MaxSizeMB: 100,
			MaxBackups: 7, MaxAgeDays: 30, Compress: true,
		},
		Wrapper: WrapperConfig{
			Address: "127.0.0.1:8080", Insecure: true, TimeoutSeconds: 30, LoginTimeoutSeconds: 120,
		},
		Catalog: CatalogConfig{
			DefaultStorefront: "us", Language: "en-US",
			MaxParallelRequests: 16, RequestsPerSecond: 10, RequestBurst: 16,
			DeveloperTokenTTLHours: 1, TokenCacheTTLHours: 12, AlbumTrackURLMode: "song", SignedModeHLSSource: "wrapper",
		},
		Download: DownloadConfig{
			QualityPriority: []string{"alac", "aac"}, CodecAlternative: true, MemoryMode: MemoryModeLow,
			MaxRunningJobs: 2, MaxParallelDownloads: 16, MaxParallelDecrypts: 32, MaxParallelWrapperRequests: 24, MaxAttempts: 4,
			DownloadsDir:       "data/downloads",
			SongPathFormat:     "songs/{ArtistName}/{AlbumName}/{TrackNumber:02d}. {SongName}",
			AlbumPathFormat:    "albums/{ArtistName}/{AlbumName}/{TrackNumber:02d}. {SongName}",
			ArtistPathFormat:   "artists/{ArtistName}/{AlbumName}/{TrackNumber:02d}. {SongName}",
			PlaylistPathFormat: "playlists/{PlaylistName}/{SongNumber:02d}. {SongName}",
			StationPathFormat:  "stations/{StationName}/{SongNumber:02d}. {SongName}",
			TempDir:            "data/tmp", CoverSize: "5000x5000", CoverFormat: "jpg",
			EmbedCover: true, EmbedLyrics: true, LyricsFormat: "lrc", LyricsType: "lyrics", LyricsExtras: []string{},
			ALACMaxSampleRate: 192000, ALACMaxBitDepth: 24, CheckIntegrity: true,
		},
		Tools:    ToolsConfig{FFmpeg: "ffmpeg"},
		Simulate: SimulateConfig{Enabled: false, MinSpeedKBps: 512, MaxSpeedKBps: 4096},
	}
}

// LoadPair reads the split configuration: the startup file (keys consumed
// once at process start) plus the runtime file (keys the config API may
// change while running), overlays any AMDL_* environment variable overrides
// on top (see env.go), and validates the merged result. Each file is checked
// strictly against its side of the split — a key in the wrong file is a load
// error, not a silently shadowed value.
func LoadPair(startupPath, runtimePath string) (Config, error) {
	return loadPair(startupPath, runtimePath, os.Environ())
}

// loadPair is LoadPair with an explicit environment so tests can inject one.
func loadPair(startupPath, runtimePath string, environ []string) (Config, error) {
	cfg := Default()
	if err := decodeFileStrict(&cfg, startupPath, roleStartup); err != nil {
		return cfg, err
	}
	if err := decodeFileStrict(&cfg, runtimePath, roleRuntime); err != nil {
		return cfg, err
	}
	if err := applyEnvOverrides(&cfg, environ); err != nil {
		return cfg, err
	}
	if err := cfg.NormalizeDeprecated(); err != nil {
		return cfg, err
	}
	// Same tolerance as the single-file path: files or AMDL_* overrides
	// written before the limits existed may hold larger values, so clamp
	// them instead of refusing to boot.
	clampCatalogLimits(&cfg.Catalog)
	clampDownloadLimits(&cfg.Download)
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Load reads a single pre-split config file holding any mix of startup and
// runtime keys, overlays AMDL_* environment overrides, and validates the
// result. Production loading goes through LoadPair; Load remains for the
// one-time EnsureFiles migration of a legacy config.yaml and for reading
// example files that still use the combined layout.
func Load(path string) (Config, error) {
	return load(path, os.Environ())
}

// load is Load with an explicit environment, so tests can inject one and
// EnsureFiles can read legacy and example files without baking overrides in.
func load(path string, environ []string) (Config, error) {
	cfg := Default()
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, err
	}
	if err := applyEnvOverrides(&cfg, environ); err != nil {
		return cfg, err
	}
	if err := cfg.NormalizeDeprecated(); err != nil {
		return cfg, err
	}
	// Config files written before the limits existed may hold larger values;
	// clamp them instead of refusing to boot. New values submitted through the
	// runtime config API still fail Validate with an explicit error.
	clampCatalogLimits(&cfg.Catalog)
	clampDownloadLimits(&cfg.Download)
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// clampCatalogLimits lowers startup-bound Apple request controls loaded from
// disk or the environment to their hard process-wide limits.
func clampCatalogLimits(c *CatalogConfig) {
	if c.MaxParallelRequests > maxGlobalPoolLimit {
		c.MaxParallelRequests = maxGlobalPoolLimit
	}
	if c.RequestsPerSecond > maxGlobalPoolLimit {
		c.RequestsPerSecond = maxGlobalPoolLimit
	}
	if c.RequestBurst > maxGlobalPoolLimit {
		c.RequestBurst = maxGlobalPoolLimit
	}
}

// clampDownloadLimits lowers the bounded download settings to their hard
// limits in place. It accepts old managed files with oversized values and
// legacy persisted job overrides whose mutable retry count predates its cap.
func clampDownloadLimits(d *DownloadConfig) {
	if d.MaxRunningJobs > maxRunningJobsLimit {
		d.MaxRunningJobs = maxRunningJobsLimit
	}
	if d.MaxParallelDownloads > maxGlobalPoolLimit {
		d.MaxParallelDownloads = maxGlobalPoolLimit
	}
	if d.MaxParallelDecrypts > maxGlobalPoolLimit {
		d.MaxParallelDecrypts = maxGlobalPoolLimit
	}
	if d.MaxParallelWrapperRequests > maxGlobalPoolLimit {
		d.MaxParallelWrapperRequests = maxGlobalPoolLimit
	}
	if d.MaxAttempts > maxAttemptsLimit {
		d.MaxAttempts = maxAttemptsLimit
	}
}

// NormalizeDeprecated accepts compatibility-only fields from older config
// files, environment variables, and API payloads, then removes them from the
// effective snapshot so the next managed-file rewrite completes the migration.
// media_user_token_priority became redundant when request tokens joined the
// ordinary per-job override layer: a present override always wins, while an
// absent one inherits catalog.media_user_token.
func (c *Config) NormalizeDeprecated() error {
	if c == nil {
		return nil
	}
	if p := c.Catalog.LegacyMediaUserTokenPriority; p != "" && p != "request" && p != "config" {
		return fmt.Errorf("catalog.media_user_token_priority must be request or config")
	}
	c.Catalog.LegacyMediaUserTokenPriority = ""
	return nil
}

// Validate checks the semantic rules every Config must satisfy, whether it
// was loaded from YAML at startup or assembled at runtime (config update API,
// per-request job overrides applied to a base config).
func (c Config) Validate() error {
	switch c.Logging.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("logging.level must be debug, info, warn, or error")
	}
	switch c.Logging.Format {
	case "text", "json":
	default:
		return fmt.Errorf("logging.format must be text or json")
	}
	if !c.Logging.Console && !c.Logging.FileEnabled && c.Logging.BufferSize == 0 {
		return fmt.Errorf("logging must enable console, file, or a non-zero buffer_size")
	}
	if c.Logging.BufferSize < 0 || c.Logging.BufferSize > 100000 {
		return fmt.Errorf("logging.buffer_size must be between 0 and 100000")
	}
	if c.Logging.FileEnabled && strings.TrimSpace(c.Logging.FilePath) == "" {
		return fmt.Errorf("logging.file_path cannot be empty when file_enabled is true")
	}
	if c.Logging.MaxSizeMB < 1 {
		return fmt.Errorf("logging.max_size_mb must be >= 1")
	}
	if c.Logging.MaxBackups < 0 {
		return fmt.Errorf("logging.max_backups must be >= 0")
	}
	if c.Logging.MaxAgeDays < 0 {
		return fmt.Errorf("logging.max_age_days must be >= 0")
	}
	if c.Catalog.AlbumTrackURLMode != "song" && c.Catalog.AlbumTrackURLMode != "album" {
		return fmt.Errorf("catalog.album_track_url_mode must be song or album")
	}
	if p := c.Catalog.LegacyMediaUserTokenPriority; p != "" && p != "request" && p != "config" {
		return fmt.Errorf("catalog.media_user_token_priority must be request or config")
	}
	if c.Catalog.SignedModeHLSSource != "wrapper" && c.Catalog.SignedModeHLSSource != "web_token" {
		return fmt.Errorf("catalog.signed_mode_hls_source must be wrapper or web_token")
	}
	signingFields := 0
	for _, v := range []string{c.Catalog.AppleMusicPrivateKeyPath, c.Catalog.AppleMusicKeyID, c.Catalog.AppleMusicTeamID} {
		if strings.TrimSpace(v) != "" {
			signingFields++
		}
	}
	if signingFields != 0 && signingFields != 3 {
		return fmt.Errorf("catalog.apple_music_private_key_path, catalog.apple_music_key_id, and catalog.apple_music_team_id must all be set together or all left empty")
	}
	for _, origin := range c.Catalog.AllowedOrigins {
		if strings.TrimSpace(origin) == "" {
			return fmt.Errorf("catalog.allowed_origins must not contain empty entries")
		}
	}
	for key, value := range map[string]int{
		"catalog.max_parallel_requests": c.Catalog.MaxParallelRequests,
		"catalog.requests_per_second":   c.Catalog.RequestsPerSecond,
		"catalog.request_burst":         c.Catalog.RequestBurst,
	} {
		if value > maxGlobalPoolLimit {
			return fmt.Errorf("%s must be at most %d", key, maxGlobalPoolLimit)
		}
	}
	for name, value := range map[string]string{
		"download.song_path_format":     c.Download.SongPathFormat,
		"download.album_path_format":    c.Download.AlbumPathFormat,
		"download.artist_path_format":   c.Download.ArtistPathFormat,
		"download.playlist_path_format": c.Download.PlaylistPathFormat,
		"download.station_path_format":  c.Download.StationPathFormat,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s cannot be empty", name)
		}
	}
	if c.Download.MaxRunningJobs > maxRunningJobsLimit {
		return fmt.Errorf("download.max_running_jobs must be at most %d", maxRunningJobsLimit)
	}
	if c.Download.MaxParallelDownloads > maxGlobalPoolLimit {
		return fmt.Errorf("download.max_parallel_downloads must be at most %d", maxGlobalPoolLimit)
	}
	if c.Download.MaxParallelDecrypts > maxGlobalPoolLimit {
		return fmt.Errorf("download.max_parallel_decrypts must be at most %d", maxGlobalPoolLimit)
	}
	if c.Download.MaxParallelWrapperRequests > maxGlobalPoolLimit {
		return fmt.Errorf("download.max_parallel_wrapper_requests must be at most %d", maxGlobalPoolLimit)
	}
	if c.Download.MaxAttempts > maxAttemptsLimit {
		return fmt.Errorf("download.max_attempts must be at most %d", maxAttemptsLimit)
	}
	switch c.Download.MemoryMode {
	case MemoryModeLow, MemoryModeHigh:
	default:
		return fmt.Errorf("download.memory_mode must be low or high")
	}
	switch c.Download.CoverFormat {
	case "jpg", "jpeg", "png":
	default:
		return fmt.Errorf("download.cover_format must be jpg, jpeg, or png")
	}
	switch c.Download.LyricsFormat {
	case "lrc", "ttml":
	default:
		return fmt.Errorf("download.lyrics_format must be lrc or ttml")
	}
	switch c.Download.LyricsType {
	case "lyrics", "syllable-lyrics":
	default:
		return fmt.Errorf("download.lyrics_type must be lyrics or syllable-lyrics")
	}
	allowedLyricsExtras := map[string]struct{}{"translation": {}, "pronunciation": {}}
	for _, extra := range c.Download.LyricsExtras {
		if _, ok := allowedLyricsExtras[extra]; !ok {
			return fmt.Errorf("download.lyrics_extras contains unsupported value %q", extra)
		}
	}
	if len(c.Download.QualityPriority) == 0 {
		return fmt.Errorf("download.quality_priority must contain at least one codec")
	}
	allowedCodecs := map[string]struct{}{
		"alac": {}, "aac": {}, "aac-binaural": {}, "aac-downmix": {}, "ec3": {}, "ac3": {},
	}
	for _, codec := range c.Download.QualityPriority {
		if _, ok := allowedCodecs[codec]; !ok {
			return fmt.Errorf("unsupported codec %q in download.quality_priority", codec)
		}
	}
	if c.Simulate.Enabled {
		if c.Simulate.MinSpeedKBps < 1 {
			return fmt.Errorf("simulate.min_speed_kbps must be >= 1 when simulate mode is enabled")
		}
		if c.Simulate.MaxSpeedKBps < c.Simulate.MinSpeedKBps {
			return fmt.Errorf("simulate.max_speed_kbps must be >= simulate.min_speed_kbps")
		}
	}
	return nil
}
