package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// DownloadOverrides is a per-request overlay for the job-mutable part of the
// runtime config. A batch submit may attach one; it is stored on each relevant
// job and applied on top of the live config when the job runs, so it survives
// retries and post-restart requeues without leaking into other jobs.
//
// Every field is optional: nil means "keep the runtime config's value". The
// Download-related JSON keys mirror the job-scoped subset of
// configs/config.yaml. MediaUserToken overlays catalog.media_user_token for a
// job that
// needs Apple Music user identity. max_running_jobs, max_parallel_downloads,
// max_parallel_decrypts, and max_parallel_wrapper_requests are deliberately
// absent — they size process-wide pools at startup and cannot apply to a
// single job.
// The two slice fields are *[]string rather than []string so an explicit
// empty list survives the JSON round trip through the jobs table: nil (field
// absent, keep config) marshals away under omitempty, while a pointer to an
// empty slice marshals as [] and still means "override to none" after the
// worker loads the job back.
type DownloadOverrides struct {
	MediaUserToken     *string   `json:"media_user_token,omitempty"`
	QualityPriority    *[]string `json:"quality_priority,omitempty"`
	CodecAlternative   *bool     `json:"codec_alternative,omitempty"`
	MemoryMode         *string   `json:"memory_mode,omitempty"`
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
	ForceOverwrite     *bool     `json:"force_overwrite,omitempty"`
}

// WithoutMediaUserToken returns a shallow copy without the credential. It is
// nil-safe and collapses an otherwise-empty override to nil, avoiding empty
// override records for jobs that never use a media-user-token.
func (o *DownloadOverrides) WithoutMediaUserToken() *DownloadOverrides {
	if o == nil || o.MediaUserToken == nil {
		return o
	}
	clean := *o
	clean.MediaUserToken = nil
	if clean == (DownloadOverrides{}) {
		return nil
	}
	return &clean
}

// Apply returns base with every non-nil job-mutable override substituted.
// Nil-safe: a nil receiver returns base unchanged.
func (o *DownloadOverrides) Apply(base Config) Config {
	if o == nil {
		return base
	}
	if o.MediaUserToken != nil {
		base.Catalog.MediaUserToken = *o.MediaUserToken
	}
	d := &base.Download
	if o.QualityPriority != nil {
		d.QualityPriority = *o.QualityPriority
	}
	if o.CodecAlternative != nil {
		d.CodecAlternative = *o.CodecAlternative
	}
	if o.MemoryMode != nil {
		d.MemoryMode = *o.MemoryMode
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
	if o.ForceOverwrite != nil {
		d.ForceOverwrite = *o.ForceOverwrite
	}
	return base
}

// ApplyValidated applies the request-scoped overrides and verifies both the
// ordinary config rules and the extra trust boundary around filesystem roots.
// A request may select a subdirectory of the administrator-configured roots,
// but it may not redirect downloads or scratch data elsewhere. Numeric
// overrides above the hard limits are clamped rather than rejected: persisted
// jobs may predate the limits, and their stored overrides re-run through this
// validation on every retry and post-restart requeue. Fresh client input
// should go through ApplyValidatedStrict instead.
func (o *DownloadOverrides) ApplyValidated(base Config) (Config, error) {
	return o.applyValidated(base, true)
}

// ApplyValidatedStrict is ApplyValidated for freshly submitted overrides:
// numeric values above the hard limits are rejected with an explicit error
// instead of clamped, matching the contract of the runtime config API.
func (o *DownloadOverrides) ApplyValidatedStrict(base Config) (Config, error) {
	return o.applyValidated(base, false)
}

func (o *DownloadOverrides) applyValidated(base Config, clampLimits bool) (Config, error) {
	applied := o.Apply(base)
	if clampLimits {
		clampDownloadLimits(&applied.Download)
	}
	if err := applied.Validate(); err != nil {
		return applied, err
	}
	if o == nil {
		return applied, nil
	}
	for _, path := range []struct {
		name      string
		override  *string
		base      string
		candidate string
	}{
		{name: "download.downloads_dir", override: o.DownloadsDir, base: base.Download.DownloadsDir, candidate: applied.Download.DownloadsDir},
		{name: "download.temp_dir", override: o.TempDir, base: base.Download.TempDir, candidate: applied.Download.TempDir},
	} {
		if path.override == nil {
			continue
		}
		contained, err := pathIsWithin(path.base, path.candidate)
		if err != nil {
			return applied, fmt.Errorf("validate %s override: %w", path.name, err)
		}
		if !contained {
			return applied, fmt.Errorf("%s override must stay within the configured root %q", path.name, path.base)
		}
	}
	return applied, nil
}

// pathIsWithin compares canonical absolute paths. EvalSymlinks alone cannot
// handle the normal case where a requested subdirectory does not exist yet,
// so canonicalPath resolves the deepest existing ancestor and appends the
// missing suffix. This catches both lexical traversal and existing symlinks
// that point outside the configured root without rejecting new subdirectories.
func pathIsWithin(base, candidate string) (bool, error) {
	basePath, err := canonicalPath(base)
	if err != nil {
		return false, err
	}
	candidatePath, err := canonicalPath(candidate)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(basePath, candidatePath)
	if err != nil {
		return false, err
	}
	return rel != ".." && !filepath.IsAbs(rel) && (rel == "." || !startsWithParent(rel)), nil
}

func startsWithParent(path string) bool {
	return len(path) >= 3 && path[:3] == ".."+string(filepath.Separator)
}

func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	current := abs
	var missing []string
	for {
		resolved, evalErr := filepath.EvalSymlinks(current)
		if evalErr == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(evalErr) {
			return "", evalErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", evalErr
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}
