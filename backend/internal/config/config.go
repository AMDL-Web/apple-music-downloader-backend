package config

import (
	"os"
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
	Address        string `yaml:"address" json:"address"`
	Insecure       bool   `yaml:"insecure" json:"insecure"`
	TimeoutSeconds int    `yaml:"timeout_seconds" json:"timeout_seconds"`
}

func (c WrapperConfig) Timeout() time.Duration {
	if c.TimeoutSeconds <= 0 {
		return 30 * time.Second
	}
	return time.Duration(c.TimeoutSeconds) * time.Second
}

type CatalogConfig struct {
	DefaultStorefront  string `yaml:"default_storefront" json:"default_storefront"`
	Language           string `yaml:"language" json:"language"`
	AuthorizationToken string `yaml:"authorization_token" json:"-"`
	TokenCacheTTLHours int    `yaml:"token_cache_ttl_hours" json:"token_cache_ttl_hours"`
	AlbumTrackURLMode  string `yaml:"album_track_url_mode" json:"album_track_url_mode"`
}

func (c CatalogConfig) TokenTTL() time.Duration {
	if c.TokenCacheTTLHours <= 0 {
		return 12 * time.Hour
	}
	return time.Duration(c.TokenCacheTTLHours) * time.Hour
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
		Wrapper:  WrapperConfig{Address: "192.168.3.42:8080", Insecure: true, TimeoutSeconds: 30},
		Catalog: CatalogConfig{
			DefaultStorefront: "us", Language: "en-US", TokenCacheTTLHours: 12, AlbumTrackURLMode: "song",
		},
		Download: DownloadConfig{
			QualityPriority: []string{"alac", "aac"}, CodecAlternative: true,
			MaxRunningJobs: 2, MaxParallelTracks: 3, Retries: 3,
			DownloadsDir: "data/downloads", SongsFolderName: "songs", AlbumsFolderName: "albums", PlaylistsFolderName: "playlists",
			TempDir: "data/tmp", CoverSize: "5000x5000", CoverFormat: "jpg",
			EmbedCover: true, EmbedLyrics: true, LyricsFormat: "lrc",
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
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}
