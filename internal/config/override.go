package config

// DownloadOverrides is a per-request overlay for the download section of the
// runtime config. A batch submit may attach one; it is stored on every job
// created from that submission and applied on top of the live config when the
// job runs, so it survives retries and post-restart requeues without leaking
// into other jobs.
//
// Every field is optional: nil means "keep the runtime config's value". The
// JSON keys mirror the download section of configs/config.yaml exactly.
// max_running_jobs is deliberately absent — it sizes the worker pool at
// startup and cannot apply to a single job.
// The two slice fields are *[]string rather than []string so an explicit
// empty list survives the JSON round trip through the jobs table: nil (field
// absent, keep config) marshals away under omitempty, while a pointer to an
// empty slice marshals as [] and still means "override to none" after the
// worker loads the job back.
type DownloadOverrides struct {
	QualityPriority    *[]string `json:"quality_priority,omitempty"`
	CodecAlternative   *bool     `json:"codec_alternative,omitempty"`
	MaxParallelTracks  *int      `json:"max_parallel_tracks,omitempty"`
	MaxAttempts        *int      `json:"max_attempts,omitempty"`
	DownloadsDir       *string   `json:"downloads_dir,omitempty"`
	SongPathFormat     *string   `json:"song_path_format,omitempty"`
	AlbumPathFormat    *string   `json:"album_path_format,omitempty"`
	ArtistPathFormat   *string   `json:"artist_path_format,omitempty"`
	PlaylistPathFormat *string   `json:"playlist_path_format,omitempty"`
	StationPathFormat  *string   `json:"station_path_format,omitempty"`
	TempDir            *string   `json:"temp_dir,omitempty"`
	CoverSize          *string   `json:"cover_size,omitempty"`
	CoverFormat        *string   `json:"cover_format,omitempty"`
	EmbedCover         *bool     `json:"embed_cover,omitempty"`
	SaveAlbumCover     *bool     `json:"save_album_cover,omitempty"`
	SaveArtistCover    *bool     `json:"save_artist_cover,omitempty"`
	SavePlaylistCover  *bool     `json:"save_playlist_cover,omitempty"`
	EmbedLyrics        *bool     `json:"embed_lyrics,omitempty"`
	SaveLyricsFile     *bool     `json:"save_lyrics_file,omitempty"`
	LyricsFormat       *string   `json:"lyrics_format,omitempty"`
	LyricsType         *string   `json:"lyrics_type,omitempty"`
	LyricsExtras       *[]string `json:"lyrics_extras,omitempty"`
	ALACMaxSampleRate  *int      `json:"alac_max_sample_rate,omitempty"`
	ALACMaxBitDepth    *int      `json:"alac_max_bit_depth,omitempty"`
	CheckIntegrity     *bool     `json:"check_integrity,omitempty"`
}

// Apply returns base with every non-nil override substituted into its
// download section. Nil-safe: a nil receiver returns base unchanged.
func (o *DownloadOverrides) Apply(base Config) Config {
	if o == nil {
		return base
	}
	d := &base.Download
	if o.QualityPriority != nil {
		d.QualityPriority = *o.QualityPriority
	}
	if o.CodecAlternative != nil {
		d.CodecAlternative = *o.CodecAlternative
	}
	if o.MaxParallelTracks != nil {
		d.MaxParallelTracks = *o.MaxParallelTracks
	}
	if o.MaxAttempts != nil {
		d.MaxAttempts = *o.MaxAttempts
	}
	if o.DownloadsDir != nil {
		d.DownloadsDir = *o.DownloadsDir
	}
	if o.SongPathFormat != nil {
		d.SongPathFormat = *o.SongPathFormat
	}
	if o.AlbumPathFormat != nil {
		d.AlbumPathFormat = *o.AlbumPathFormat
	}
	if o.ArtistPathFormat != nil {
		d.ArtistPathFormat = *o.ArtistPathFormat
	}
	if o.PlaylistPathFormat != nil {
		d.PlaylistPathFormat = *o.PlaylistPathFormat
	}
	if o.StationPathFormat != nil {
		d.StationPathFormat = *o.StationPathFormat
	}
	if o.TempDir != nil {
		d.TempDir = *o.TempDir
	}
	if o.CoverSize != nil {
		d.CoverSize = *o.CoverSize
	}
	if o.CoverFormat != nil {
		d.CoverFormat = *o.CoverFormat
	}
	if o.EmbedCover != nil {
		d.EmbedCover = *o.EmbedCover
	}
	if o.SaveAlbumCover != nil {
		d.SaveAlbumCover = *o.SaveAlbumCover
	}
	if o.SaveArtistCover != nil {
		d.SaveArtistCover = *o.SaveArtistCover
	}
	if o.SavePlaylistCover != nil {
		d.SavePlaylistCover = *o.SavePlaylistCover
	}
	if o.EmbedLyrics != nil {
		d.EmbedLyrics = *o.EmbedLyrics
	}
	if o.SaveLyricsFile != nil {
		d.SaveLyricsFile = *o.SaveLyricsFile
	}
	if o.LyricsFormat != nil {
		d.LyricsFormat = *o.LyricsFormat
	}
	if o.LyricsType != nil {
		d.LyricsType = *o.LyricsType
	}
	if o.LyricsExtras != nil {
		d.LyricsExtras = *o.LyricsExtras
	}
	if o.ALACMaxSampleRate != nil {
		d.ALACMaxSampleRate = *o.ALACMaxSampleRate
	}
	if o.ALACMaxBitDepth != nil {
		d.ALACMaxBitDepth = *o.ALACMaxBitDepth
	}
	if o.CheckIntegrity != nil {
		d.CheckIntegrity = *o.CheckIntegrity
	}
	return base
}
