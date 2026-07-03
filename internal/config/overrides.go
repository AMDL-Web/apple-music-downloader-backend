package config

import (
	"fmt"
	"regexp"

	"amdl/internal/domain"
)

var coverSizePattern = regexp.MustCompile(`^[1-9][0-9]{1,4}x[1-9][0-9]{1,4}$`)

// ApplyDownloadOverrides returns base with every non-nil override field
// applied on top. Slice fields are copied so the returned config never
// aliases the overlay's backing arrays.
func ApplyDownloadOverrides(base DownloadConfig, o *domain.DownloadOverrides) DownloadConfig {
	if o == nil {
		return base
	}
	if o.QualityPriority != nil {
		base.QualityPriority = append([]string(nil), (*o.QualityPriority)...)
	}
	if o.CodecAlternative != nil {
		base.CodecAlternative = *o.CodecAlternative
	}
	if o.MaxParallelTracks != nil {
		base.MaxParallelTracks = *o.MaxParallelTracks
	}
	if o.Retries != nil {
		base.Retries = *o.Retries
	}
	if o.SongsFolderName != nil {
		base.SongsFolderName = *o.SongsFolderName
	}
	if o.AlbumsFolderName != nil {
		base.AlbumsFolderName = *o.AlbumsFolderName
	}
	if o.PlaylistsFolderName != nil {
		base.PlaylistsFolderName = *o.PlaylistsFolderName
	}
	if o.ArtistsFolderName != nil {
		base.ArtistsFolderName = *o.ArtistsFolderName
	}
	if o.CoverSize != nil {
		base.CoverSize = *o.CoverSize
	}
	if o.CoverFormat != nil {
		base.CoverFormat = *o.CoverFormat
	}
	if o.EmbedCover != nil {
		base.EmbedCover = *o.EmbedCover
	}
	if o.SaveAlbumCover != nil {
		base.SaveAlbumCover = *o.SaveAlbumCover
	}
	if o.SaveArtistCover != nil {
		base.SaveArtistCover = *o.SaveArtistCover
	}
	if o.SavePlaylistCover != nil {
		base.SavePlaylistCover = *o.SavePlaylistCover
	}
	if o.EmbedLyrics != nil {
		base.EmbedLyrics = *o.EmbedLyrics
	}
	if o.SaveLyricsFile != nil {
		base.SaveLyricsFile = *o.SaveLyricsFile
	}
	if o.LyricsFormat != nil {
		base.LyricsFormat = *o.LyricsFormat
	}
	if o.LyricsType != nil {
		base.LyricsType = *o.LyricsType
	}
	if o.LyricsExtras != nil {
		base.LyricsExtras = append([]string(nil), (*o.LyricsExtras)...)
	}
	if o.ArtistFolderFormat != nil {
		base.ArtistFolderFormat = *o.ArtistFolderFormat
	}
	if o.AlbumFolderFormat != nil {
		base.AlbumFolderFormat = *o.AlbumFolderFormat
	}
	if o.SongFileFormat != nil {
		base.SongFileFormat = *o.SongFileFormat
	}
	if o.PlaylistFolderFormat != nil {
		base.PlaylistFolderFormat = *o.PlaylistFolderFormat
	}
	if o.PlaylistSongFileFormat != nil {
		base.PlaylistSongFileFormat = *o.PlaylistSongFileFormat
	}
	if o.ALACMaxSampleRate != nil {
		base.ALACMaxSampleRate = *o.ALACMaxSampleRate
	}
	if o.ALACMaxBitDepth != nil {
		base.ALACMaxBitDepth = *o.ALACMaxBitDepth
	}
	if o.CheckIntegrity != nil {
		base.CheckIntegrity = *o.CheckIntegrity
	}
	return base
}

// ValidateDownloadOverrides rejects an overlay whose fields would produce an
// invalid effective config. Overlays come from API callers, so numeric fields
// additionally get bounds the trusted global config does not enforce.
func ValidateDownloadOverrides(o *domain.DownloadOverrides) error {
	if o == nil {
		return nil
	}
	if err := validateDownload(ApplyDownloadOverrides(Default().Download, o)); err != nil {
		return err
	}
	if o.MaxParallelTracks != nil && (*o.MaxParallelTracks < 1 || *o.MaxParallelTracks > 16) {
		return fmt.Errorf("download.max_parallel_tracks override must be between 1 and 16")
	}
	if o.Retries != nil && (*o.Retries < 0 || *o.Retries > 10) {
		return fmt.Errorf("download.retries override must be between 0 and 10")
	}
	if o.ALACMaxSampleRate != nil && *o.ALACMaxSampleRate <= 0 {
		return fmt.Errorf("download.alac_max_sample_rate override must be positive")
	}
	if o.ALACMaxBitDepth != nil && *o.ALACMaxBitDepth <= 0 {
		return fmt.Errorf("download.alac_max_bit_depth override must be positive")
	}
	if o.CoverSize != nil && !coverSizePattern.MatchString(*o.CoverSize) {
		return fmt.Errorf("download.cover_size override must look like 5000x5000")
	}
	return nil
}
