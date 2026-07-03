package domain

import (
	"bytes"
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"time"
)

type Role string

const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
)

var usernamePattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// ValidUsername reports whether v is safe to use as a username and as a
// per-user download directory name.
func ValidUsername(v string) bool { return usernamePattern.MatchString(v) }

type User struct {
	ID        string             `json:"id"`
	Username  string             `json:"username"`
	Role      Role               `json:"role"`
	AvatarURL string             `json:"avatar_url"`
	Enabled   bool               `json:"enabled"`
	Aliases   []string           `json:"aliases"`
	Emails    []string           `json:"emails"`
	Overrides *DownloadOverrides `json:"config,omitempty"`
	CreatedAt time.Time          `json:"created_at"`
	UpdatedAt time.Time          `json:"updated_at"`
}

// DownloadOverrides is a sparse overlay over the download section of the
// global config: nil fields inherit from the layer below. Layers stack as
// global config < per-user overrides < per-request overrides. System-owned
// fields (downloads_dir, temp_dir, max_running_jobs) are intentionally
// absent — they cannot be overridden by users or requests.
type DownloadOverrides struct {
	QualityPriority        *[]string `json:"quality_priority,omitempty"`
	CodecAlternative       *bool     `json:"codec_alternative,omitempty"`
	MaxParallelTracks      *int      `json:"max_parallel_tracks,omitempty"`
	Retries                *int      `json:"retries,omitempty"`
	SongsFolderName        *string   `json:"songs_folder_name,omitempty"`
	AlbumsFolderName       *string   `json:"albums_folder_name,omitempty"`
	PlaylistsFolderName    *string   `json:"playlists_folder_name,omitempty"`
	ArtistsFolderName      *string   `json:"artists_folder_name,omitempty"`
	CoverSize              *string   `json:"cover_size,omitempty"`
	CoverFormat            *string   `json:"cover_format,omitempty"`
	EmbedCover             *bool     `json:"embed_cover,omitempty"`
	SaveAlbumCover         *bool     `json:"save_album_cover,omitempty"`
	SaveArtistCover        *bool     `json:"save_artist_cover,omitempty"`
	SavePlaylistCover      *bool     `json:"save_playlist_cover,omitempty"`
	EmbedLyrics            *bool     `json:"embed_lyrics,omitempty"`
	SaveLyricsFile         *bool     `json:"save_lyrics_file,omitempty"`
	LyricsFormat           *string   `json:"lyrics_format,omitempty"`
	LyricsType             *string   `json:"lyrics_type,omitempty"`
	LyricsExtras           *[]string `json:"lyrics_extras,omitempty"`
	ArtistFolderFormat     *string   `json:"artist_folder_format,omitempty"`
	AlbumFolderFormat      *string   `json:"album_folder_format,omitempty"`
	SongFileFormat         *string   `json:"song_file_format,omitempty"`
	PlaylistFolderFormat   *string   `json:"playlist_folder_format,omitempty"`
	PlaylistSongFileFormat *string   `json:"playlist_song_file_format,omitempty"`
	ALACMaxSampleRate      *int      `json:"alac_max_sample_rate,omitempty"`
	ALACMaxBitDepth        *int      `json:"alac_max_bit_depth,omitempty"`
	CheckIntegrity         *bool     `json:"check_integrity,omitempty"`
}

// IsZero reports whether no field is set, i.e. the overlay is a no-op.
// Every field is a pointer, so the struct is directly comparable.
func (o *DownloadOverrides) IsZero() bool {
	return o == nil || *o == DownloadOverrides{}
}

// DownloadOverrideKeys returns the JSON keys of every overridable download
// field. It is derived from the struct, so it stays correct as fields are
// added; callers use it as the allowlist of user-visible download settings.
func DownloadOverrideKeys() []string {
	t := reflect.TypeOf(DownloadOverrides{})
	keys := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		keys = append(keys, strings.SplitN(tag, ",", 2)[0])
	}
	return keys
}

// ParseDownloadOverrides decodes a JSON overlay, rejecting unknown fields so
// a typo in a key fails loudly instead of being silently ignored. Empty,
// "null", and "{}" input all yield nil (no overrides).
func ParseDownloadOverrides(raw []byte) (*DownloadOverrides, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	var o DownloadOverrides
	if err := decoder.Decode(&o); err != nil {
		return nil, err
	}
	if o.IsZero() {
		return nil, nil
	}
	return &o, nil
}

// MergeDownloadOverrides flattens layers into one overlay; later layers win
// field-by-field. Returns nil when the result sets nothing.
func MergeDownloadOverrides(layers ...*DownloadOverrides) *DownloadOverrides {
	var out DownloadOverrides
	for _, layer := range layers {
		if layer == nil {
			continue
		}
		if layer.QualityPriority != nil {
			out.QualityPriority = layer.QualityPriority
		}
		if layer.CodecAlternative != nil {
			out.CodecAlternative = layer.CodecAlternative
		}
		if layer.MaxParallelTracks != nil {
			out.MaxParallelTracks = layer.MaxParallelTracks
		}
		if layer.Retries != nil {
			out.Retries = layer.Retries
		}
		if layer.SongsFolderName != nil {
			out.SongsFolderName = layer.SongsFolderName
		}
		if layer.AlbumsFolderName != nil {
			out.AlbumsFolderName = layer.AlbumsFolderName
		}
		if layer.PlaylistsFolderName != nil {
			out.PlaylistsFolderName = layer.PlaylistsFolderName
		}
		if layer.ArtistsFolderName != nil {
			out.ArtistsFolderName = layer.ArtistsFolderName
		}
		if layer.CoverSize != nil {
			out.CoverSize = layer.CoverSize
		}
		if layer.CoverFormat != nil {
			out.CoverFormat = layer.CoverFormat
		}
		if layer.EmbedCover != nil {
			out.EmbedCover = layer.EmbedCover
		}
		if layer.SaveAlbumCover != nil {
			out.SaveAlbumCover = layer.SaveAlbumCover
		}
		if layer.SaveArtistCover != nil {
			out.SaveArtistCover = layer.SaveArtistCover
		}
		if layer.SavePlaylistCover != nil {
			out.SavePlaylistCover = layer.SavePlaylistCover
		}
		if layer.EmbedLyrics != nil {
			out.EmbedLyrics = layer.EmbedLyrics
		}
		if layer.SaveLyricsFile != nil {
			out.SaveLyricsFile = layer.SaveLyricsFile
		}
		if layer.LyricsFormat != nil {
			out.LyricsFormat = layer.LyricsFormat
		}
		if layer.LyricsType != nil {
			out.LyricsType = layer.LyricsType
		}
		if layer.LyricsExtras != nil {
			out.LyricsExtras = layer.LyricsExtras
		}
		if layer.ArtistFolderFormat != nil {
			out.ArtistFolderFormat = layer.ArtistFolderFormat
		}
		if layer.AlbumFolderFormat != nil {
			out.AlbumFolderFormat = layer.AlbumFolderFormat
		}
		if layer.SongFileFormat != nil {
			out.SongFileFormat = layer.SongFileFormat
		}
		if layer.PlaylistFolderFormat != nil {
			out.PlaylistFolderFormat = layer.PlaylistFolderFormat
		}
		if layer.PlaylistSongFileFormat != nil {
			out.PlaylistSongFileFormat = layer.PlaylistSongFileFormat
		}
		if layer.ALACMaxSampleRate != nil {
			out.ALACMaxSampleRate = layer.ALACMaxSampleRate
		}
		if layer.ALACMaxBitDepth != nil {
			out.ALACMaxBitDepth = layer.ALACMaxBitDepth
		}
		if layer.CheckIntegrity != nil {
			out.CheckIntegrity = layer.CheckIntegrity
		}
	}
	if out.IsZero() {
		return nil
	}
	return &out
}

type JobStatus string

const (
	JobQueued    JobStatus = "queued"
	JobRunning   JobStatus = "running"
	JobCompleted JobStatus = "completed"
	JobFailed    JobStatus = "failed"
	JobCancelled JobStatus = "cancelled"
)

type ItemStatus string

const (
	ItemQueued      ItemStatus = "queued"
	ItemResolving   ItemStatus = "resolving"
	ItemDownloading ItemStatus = "downloading"
	ItemDecrypting  ItemStatus = "decrypting"
	ItemRemuxing    ItemStatus = "remuxing"
	ItemTagging     ItemStatus = "tagging"
	ItemSaving      ItemStatus = "saving"
	ItemCompleted   ItemStatus = "completed"
	ItemFailed      ItemStatus = "failed"
	ItemSkipped     ItemStatus = "skipped_existing"
	ItemCancelled   ItemStatus = "cancelled"
)

type Job struct {
	ID           string `json:"id"`
	UserID       string `json:"user_id,omitempty"`
	Username     string `json:"username,omitempty"`
	Input        string `json:"input"`
	Type         string `json:"type"`
	Storefront   string `json:"storefront,omitempty"`
	CanonicalKey string `json:"-"`
	Force        bool   `json:"force"`
	// Overrides is the user+request config overlay snapshotted at submit
	// time, so the job downloads the same way after user-config edits or a
	// backend restart. Nil means the global config applies unchanged.
	Overrides   *DownloadOverrides `json:"config,omitempty"`
	Status      JobStatus          `json:"status"`
	TotalItems  int                `json:"total_items"`
	DoneItems   int                `json:"done_items"`
	FailedItems int                `json:"failed_items"`
	Error       string             `json:"error,omitempty"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
}

type JobItem struct {
	ID            string     `json:"id"`
	JobID         string     `json:"job_id"`
	AdamID        string     `json:"adam_id"`
	Kind          string     `json:"kind"`
	Index         int        `json:"index"`
	Title         string     `json:"title,omitempty"`
	Artist        string     `json:"artist,omitempty"`
	Album         string     `json:"album,omitempty"`
	Status        ItemStatus `json:"status"`
	Progress      float64    `json:"progress"`
	Codec         string     `json:"codec,omitempty"`
	RetryKind     string     `json:"retry_kind,omitempty"`
	Attempt       int        `json:"attempt,omitempty"`
	MaxAttempts   int        `json:"max_attempts,omitempty"`
	StatusMessage string     `json:"status_message,omitempty"`
	OutputPath    string     `json:"output_path,omitempty"`
	Error         string     `json:"error,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type Event struct {
	ID        int64     `json:"id"`
	JobID     string    `json:"job_id"`
	ItemID    string    `json:"item_id,omitempty"`
	Type      string    `json:"type"`
	Phase     string    `json:"phase,omitempty"`
	Message   string    `json:"message,omitempty"`
	Payload   string    `json:"payload,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type DownloadRequest struct {
	URLs  []string `json:"urls"`
	Force bool     `json:"force"`
	// Config carries request-level download overrides; it is kept raw so it
	// can be decoded strictly (unknown fields rejected) by
	// ParseDownloadOverrides instead of the lenient request decoder.
	Config json.RawMessage `json:"config,omitempty"`
}

type SubmitStatus string

const (
	SubmitAccepted           SubmitStatus = "accepted"
	SubmitInvalid            SubmitStatus = "invalid"
	SubmitDuplicateInRequest SubmitStatus = "duplicate_in_request"
	SubmitDuplicateActive    SubmitStatus = "duplicate_active"
	SubmitQueueFull          SubmitStatus = "queue_full"
)

type SubmitResult struct {
	URL           string       `json:"url"`
	Status        SubmitStatus `json:"status"`
	Job           *Job         `json:"job,omitempty"`
	ExistingJobID string       `json:"existing_job_id,omitempty"`
	Error         string       `json:"error,omitempty"`
}

type BatchSubmitResponse struct {
	Accepted int            `json:"accepted"`
	Rejected int            `json:"rejected"`
	Results  []SubmitResult `json:"results"`
}

type Capability struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Path      string `json:"path,omitempty"`
	Error     string `json:"error,omitempty"`
}
