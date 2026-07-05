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
	Wrapper  WrapperConfig  `yaml:"wrapper" json:"wrapper"`
	Catalog  CatalogConfig  `yaml:"catalog" json:"catalog"`
	Download DownloadConfig `yaml:"download" json:"download"`
	Tools    ToolsConfig    `yaml:"tools" json:"tools"`
}

type ServerConfig struct {
	Listen string `yaml:"listen" json:"listen"`
}

type DatabaseConfig struct {
	Path string `yaml:"path" json:"path"`
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
	DefaultStorefront        string `yaml:"default_storefront" json:"default_storefront"`
	Language                 string `yaml:"language" json:"language"`
	AppleMusicPrivateKeyPath string `yaml:"apple_music_private_key_path" json:"apple_music_private_key_path"`
	AppleMusicKeyID          string `yaml:"apple_music_key_id" json:"apple_music_key_id"`
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
	QualityPriority        []string `yaml:"quality_priority" json:"quality_priority"`
	CodecAlternative       bool     `yaml:"codec_alternative" json:"codec_alternative"`
	MaxRunningJobs         int      `yaml:"max_running_jobs" json:"max_running_jobs"`
	MaxParallelTracks      int      `yaml:"max_parallel_tracks" json:"max_parallel_tracks"`
	Retries                int      `yaml:"retries" json:"retries"`
	DownloadsDir           string   `yaml:"downloads_dir" json:"downloads_dir"`
	SongsFolderName        string   `yaml:"songs_folder_name" json:"songs_folder_name"`
	AlbumsFolderName       string   `yaml:"albums_folder_name" json:"albums_folder_name"`
	PlaylistsFolderName    string   `yaml:"playlists_folder_name" json:"playlists_folder_name"`
	ArtistsFolderName      string   `yaml:"artists_folder_name" json:"artists_folder_name"`
	TempDir                string   `yaml:"temp_dir" json:"temp_dir"`
	CoverSize              string   `yaml:"cover_size" json:"cover_size"`
	CoverFormat            string   `yaml:"cover_format" json:"cover_format"`
	EmbedCover             bool     `yaml:"embed_cover" json:"embed_cover"`
	SaveAlbumCover         bool     `yaml:"save_album_cover" json:"save_album_cover"`
	SaveArtistCover        bool     `yaml:"save_artist_cover" json:"save_artist_cover"`
	SavePlaylistCover      bool     `yaml:"save_playlist_cover" json:"save_playlist_cover"`
	EmbedLyrics            bool     `yaml:"embed_lyrics" json:"embed_lyrics"`
	SaveLyricsFile         bool     `yaml:"save_lyrics_file" json:"save_lyrics_file"`
	LyricsFormat           string   `yaml:"lyrics_format" json:"lyrics_format"`
	LyricsType             string   `yaml:"lyrics_type" json:"lyrics_type"`
	LyricsExtras           []string `yaml:"lyrics_extras" json:"lyrics_extras"`
	ArtistFolderFormat     string   `yaml:"artist_folder_format" json:"artist_folder_format"`
	AlbumFolderFormat      string   `yaml:"album_folder_format" json:"album_folder_format"`
	SongFileFormat         string   `yaml:"song_file_format" json:"song_file_format"`
	PlaylistFolderFormat   string   `yaml:"playlist_folder_format" json:"playlist_folder_format"`
	PlaylistSongFileFormat string   `yaml:"playlist_song_file_format" json:"playlist_song_file_format"`
	ALACMaxSampleRate      int      `yaml:"alac_max_sample_rate" json:"alac_max_sample_rate"`
	ALACMaxBitDepth        int      `yaml:"alac_max_bit_depth" json:"alac_max_bit_depth"`
	CheckIntegrity         bool     `yaml:"check_integrity" json:"check_integrity"`
}

type ToolsConfig struct {
	FFmpeg     string `yaml:"ffmpeg" json:"ffmpeg"`
	GPAC       string `yaml:"gpac" json:"gpac"`
	MP4Box     string `yaml:"mp4box" json:"mp4box"`
	MP4Extract string `yaml:"mp4extract" json:"mp4extract"`
	MP4Edit    string `yaml:"mp4edit" json:"mp4edit"`
}

func Default() Config {
	return Config{
		Server:   ServerConfig{Listen: "127.0.0.1:18080"},
		Database: DatabaseConfig{Path: "data/amdl.db"},
		Wrapper: WrapperConfig{
			Address: "192.168.3.42:8080", Insecure: true, TimeoutSeconds: 30, LoginTimeoutSeconds: 120,
		},
		Catalog: CatalogConfig{
			DefaultStorefront: "us", Language: "en-US", DeveloperTokenTTLHours: 1, TokenCacheTTLHours: 12, AlbumTrackURLMode: "song",
		},
		Download: DownloadConfig{
			QualityPriority: []string{"alac", "aac"}, CodecAlternative: true,
			MaxRunningJobs: 2, MaxParallelTracks: 3, Retries: 3,
			DownloadsDir: "data/downloads", SongsFolderName: "songs", AlbumsFolderName: "albums", PlaylistsFolderName: "playlists", ArtistsFolderName: "artists",
			TempDir: "data/tmp", CoverSize: "5000x5000", CoverFormat: "jpg",
			EmbedCover: true, EmbedLyrics: true, LyricsFormat: "lrc", LyricsType: "lyrics", LyricsExtras: []string{},
			ArtistFolderFormat: "{ArtistName}", AlbumFolderFormat: "{AlbumName}", SongFileFormat: "{TrackNumber:02d}. {SongName}",
			PlaylistFolderFormat: "{PlaylistName}", PlaylistSongFileFormat: "{SongNumer:02d}. {ArtistName} - {SongName}",
			ALACMaxSampleRate: 192000, ALACMaxBitDepth: 24, CheckIntegrity: true,
		},
		Tools: ToolsConfig{FFmpeg: "ffmpeg", GPAC: "gpac", MP4Box: "MP4Box", MP4Extract: "mp4extract", MP4Edit: "mp4edit"},
	}
}

func Load(path string) (Config, error) {
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
	if err := cfg.validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c Config) validate() error {
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
		"download.songs_folder_name":         c.Download.SongsFolderName,
		"download.albums_folder_name":        c.Download.AlbumsFolderName,
		"download.playlists_folder_name":     c.Download.PlaylistsFolderName,
		"download.artists_folder_name":       c.Download.ArtistsFolderName,
		"download.artist_folder_format":      c.Download.ArtistFolderFormat,
		"download.album_folder_format":       c.Download.AlbumFolderFormat,
		"download.song_file_format":          c.Download.SongFileFormat,
		"download.playlist_folder_format":    c.Download.PlaylistFolderFormat,
		"download.playlist_song_file_format": c.Download.PlaylistSongFileFormat,
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
	return nil
}
