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
	AppleMusicPrivateKeyPath string   `yaml:"apple_music_private_key_path" json:"apple_music_private_key_path"`
	AppleMusicKeyID          string   `yaml:"apple_music_key_id" json:"apple_music_key_id"`
	AppleMusicTeamID         string   `yaml:"apple_music_team_id" json:"apple_music_team_id"`
	DeveloperTokenTTLHours   int      `yaml:"developer_token_ttl_hours" json:"developer_token_ttl_hours"`
	AllowedOrigins           []string `yaml:"allowed_origins" json:"allowed_origins"`
	TokenCacheTTLHours       int      `yaml:"token_cache_ttl_hours" json:"token_cache_ttl_hours"`
	AlbumTrackURLMode        string   `yaml:"album_track_url_mode" json:"album_track_url_mode"`
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

type DownloadConfig struct {
	QualityPriority    []string `yaml:"quality_priority" json:"quality_priority"`
	CodecAlternative   bool     `yaml:"codec_alternative" json:"codec_alternative"`
	MaxRunningJobs     int      `yaml:"max_running_jobs" json:"max_running_jobs"`
	MaxParallelTracks  int      `yaml:"max_parallel_tracks" json:"max_parallel_tracks"`
	MaxAttempts        int      `yaml:"max_attempts" json:"max_attempts"`
	DownloadsDir       string   `yaml:"downloads_dir" json:"downloads_dir"`
	SongPathFormat     string   `yaml:"song_path_format" json:"song_path_format"`
	AlbumPathFormat    string   `yaml:"album_path_format" json:"album_path_format"`
	ArtistPathFormat   string   `yaml:"artist_path_format" json:"artist_path_format"`
	PlaylistPathFormat string   `yaml:"playlist_path_format" json:"playlist_path_format"`
	TempDir            string   `yaml:"temp_dir" json:"temp_dir"`
	CoverSize          string   `yaml:"cover_size" json:"cover_size"`
	CoverFormat        string   `yaml:"cover_format" json:"cover_format"`
	EmbedCover         bool     `yaml:"embed_cover" json:"embed_cover"`
	SaveAlbumCover     bool     `yaml:"save_album_cover" json:"save_album_cover"`
	SaveArtistCover    bool     `yaml:"save_artist_cover" json:"save_artist_cover"`
	SavePlaylistCover  bool     `yaml:"save_playlist_cover" json:"save_playlist_cover"`
	EmbedLyrics        bool     `yaml:"embed_lyrics" json:"embed_lyrics"`
	SaveLyricsFile     bool     `yaml:"save_lyrics_file" json:"save_lyrics_file"`
	LyricsFormat       string   `yaml:"lyrics_format" json:"lyrics_format"`
	LyricsType         string   `yaml:"lyrics_type" json:"lyrics_type"`
	LyricsExtras       []string `yaml:"lyrics_extras" json:"lyrics_extras"`
	ALACMaxSampleRate  int      `yaml:"alac_max_sample_rate" json:"alac_max_sample_rate"`
	ALACMaxBitDepth    int      `yaml:"alac_max_bit_depth" json:"alac_max_bit_depth"`
	CheckIntegrity     bool     `yaml:"check_integrity" json:"check_integrity"`
}

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
			Level: "info", Format: "text", Console: true, AccessLog: true,
			BufferSize: 2000, FilePath: "data/logs/amdl.log", MaxSizeMB: 100,
			MaxBackups: 7, MaxAgeDays: 30, Compress: true,
		},
		Wrapper: WrapperConfig{
			Address: "127.0.0.1:8080", Insecure: true, TimeoutSeconds: 30, LoginTimeoutSeconds: 120,
		},
		Catalog: CatalogConfig{
			DefaultStorefront: "us", Language: "en-US", DeveloperTokenTTLHours: 1, TokenCacheTTLHours: 12, AlbumTrackURLMode: "song",
		},
		Download: DownloadConfig{
			QualityPriority: []string{"alac", "aac"}, CodecAlternative: true,
			MaxRunningJobs: 2, MaxParallelTracks: 3, MaxAttempts: 4,
			DownloadsDir:       "data/downloads",
			SongPathFormat:     "songs/{ArtistName}/{AlbumName}/{TrackNumber:02d}. {SongName}",
			AlbumPathFormat:    "albums/{ArtistName}/{AlbumName}/{TrackNumber:02d}. {SongName}",
			ArtistPathFormat:   "artists/{ArtistName}/{AlbumName}/{TrackNumber:02d}. {SongName}",
			PlaylistPathFormat: "playlists/{PlaylistName}/{SongNumber:02d}. {SongName}",
			TempDir:            "data/tmp", CoverSize: "5000x5000", CoverFormat: "jpg",
			EmbedCover: true, EmbedLyrics: true, LyricsFormat: "lrc", LyricsType: "lyrics", LyricsExtras: []string{},
			ALACMaxSampleRate: 192000, ALACMaxBitDepth: 24, CheckIntegrity: true,
		},
		Tools:    ToolsConfig{FFmpeg: "ffmpeg"},
		Simulate: SimulateConfig{Enabled: false, MinSpeedKBps: 512, MaxSpeedKBps: 4096},
	}
}

// Load reads the config file at path, overlays any AMDL_* environment
// variable overrides on top (see env.go), and validates the result. The
// environment is re-applied on every call, so Store.Reload keeps overridden
// values pinned across file re-reads.
func Load(path string) (Config, error) {
	return load(path, os.Environ())
}

// load is Load with an explicit environment, so tests can inject one and
// BootstrapFromExample can read the example file without any overrides.
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
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Validate checks the semantic rules every Config must satisfy, whether it
// was loaded from YAML at startup or assembled at runtime (config update API,
// per-request download overrides applied to a base config).
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
	for name, value := range map[string]string{
		"download.song_path_format":     c.Download.SongPathFormat,
		"download.album_path_format":    c.Download.AlbumPathFormat,
		"download.artist_path_format":   c.Download.ArtistPathFormat,
		"download.playlist_path_format": c.Download.PlaylistPathFormat,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s cannot be empty", name)
		}
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
