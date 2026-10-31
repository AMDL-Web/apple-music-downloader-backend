package client

import (
	"net/url"
	"strconv"
	"strings"
	"time"

	"amdl/internal/domain"
)

// The response envelopes below mirror schemas of internal/api/openapi.yaml
// whose Go types are unexported in internal/api. Jobs, items and events reuse
// the domain types the backend marshals.

// Health is the HealthResponse schema.
type Health struct {
	Status        string `json:"status"`
	DatabaseError string `json:"database_error,omitempty"`
}

// SystemInfo is the SystemInfo schema.
type SystemInfo struct {
	Version              string    `json:"version"`
	Commit               string    `json:"commit"`
	BuildDate            string    `json:"build_date"`
	GoVersion            string    `json:"go_version"`
	OS                   string    `json:"os"`
	Arch                 string    `json:"arch"`
	StartedAt            time.Time `json:"started_at"`
	UptimeSeconds        int64     `json:"uptime_seconds"`
	Simulate             bool      `json:"simulate"`
	MotionArtworkEnabled bool      `json:"motion_artwork_enabled"`
	DefaultStorefront    string    `json:"default_storefront"`
	Jobs                 struct {
		Workers         int `json:"workers"`
		Active          int `json:"active"`
		QueueDepth      int `json:"queue_depth"`
		QueueCapacity   int `json:"queue_capacity"`
		RecoveryBacklog int `json:"recovery_backlog"`
	} `json:"jobs"`
	Pools struct {
		Downloads       int `json:"downloads"`
		Decrypts        int `json:"decrypts"`
		WrapperRequests int `json:"wrapper_requests"`
	} `json:"pools"`
	Tools   []domain.Capability `json:"tools"`
	Storage []Storage           `json:"storage"`
}

// Storage is one entry of SystemInfo.storage: the free space of
// downloads_dir or temp_dir, or why it could not be read.
type Storage struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	FreeBytes  int64  `json:"free_bytes,omitempty"`
	TotalBytes int64  `json:"total_bytes,omitempty"`
	Error      string `json:"error,omitempty"`
}

// WrapperStatus is the WrapperStatus schema.
type WrapperStatus struct {
	Ready          bool     `json:"ready"`
	LoggedIn       bool     `json:"logged_in"`
	Regions        []string `json:"regions"`
	Accounts       []string `json:"accounts"`
	LoginSupported bool     `json:"login_supported"`
	// Managed is set when the backend runs the wrapper on its machine.
	Managed *ManagedWrapper `json:"managed,omitempty"`
	// MediaUserToken is the wrapper's media-user-token masked, "" when the
	// backend cannot read one.
	MediaUserToken string `json:"media_user_token"`
}

// ManagedWrapper is the ManagedWrapper schema.
type ManagedWrapper struct {
	Mode          string `json:"mode"`
	Accel         string `json:"accel"`
	AccelFallback bool   `json:"accel_fallback"`
	QEMU          string `json:"qemu"`
	Guest         string `json:"guest"`
	LoginResidue  bool   `json:"login_residue"`
	LastError     string `json:"last_error,omitempty"`
}

// DownloadList is the DownloadList schema.
type DownloadList struct {
	Downloads    []domain.Job           `json:"downloads"`
	StatusCounts domain.JobStatusCounts `json:"status_counts"`
	Total        int                    `json:"total"`
	Limit        int                    `json:"limit"`
	Offset       int                    `json:"offset"`
	EventCursor  int64                  `json:"event_cursor"`
}

// DownloadDetail is the DownloadDetail schema.
type DownloadDetail struct {
	Job         domain.Job         `json:"job"`
	Items       []domain.JobItem   `json:"items"`
	Hooks       []domain.HookState `json:"hooks"`
	EventCursor int64              `json:"event_cursor"`
}

// HooksRunning reports whether a post-download hook of the job is still
// running, which can outlast the job's terminal status.
func (d DownloadDetail) HooksRunning() bool {
	for _, h := range d.Hooks {
		if h.Status == "running" {
			return true
		}
	}
	return false
}

// Lookup is the LookupResponse schema: a parsed link and its catalog
// metadata, with the Job field names.
type Lookup struct {
	Input               string   `json:"input"`
	Type                string   `json:"type"`
	Storefront          string   `json:"storefront"`
	ID                  string   `json:"id"`
	TrackID             string   `json:"track_id,omitempty"`
	Title               string   `json:"title"`
	ArtistName          string   `json:"artist_name,omitempty"`
	ArtistURL           string   `json:"artist_url,omitempty"`
	CuratorName         string   `json:"curator_name,omitempty"`
	AlbumName           string   `json:"album_name,omitempty"`
	ArtworkURL          string   `json:"artwork_url,omitempty"`
	ArtworkBgColor      string   `json:"artwork_bg_color,omitempty"`
	ArtworkTextColor1   string   `json:"artwork_text_color1,omitempty"`
	ArtworkTextColor2   string   `json:"artwork_text_color2,omitempty"`
	ArtworkTextColor3   string   `json:"artwork_text_color3,omitempty"`
	ArtworkTextColor4   string   `json:"artwork_text_color4,omitempty"`
	ReleaseDate         string   `json:"release_date,omitempty"`
	Genre               string   `json:"genre,omitempty"`
	TrackCount          int      `json:"track_count,omitempty"`
	DurationMS          int      `json:"duration_ms,omitempty"`
	ContentRating       string   `json:"content_rating,omitempty"`
	AppleDigitalMaster  bool     `json:"apple_digital_master,omitempty"`
	AudioTraits         []string `json:"audio_traits,omitempty"`
	NeedsMediaUserToken bool     `json:"needs_media_user_token,omitempty"`
}

// ListQuery is the query of GET /api/v1/downloads; zero fields are left out.
type ListQuery struct {
	Limit  int
	Offset int
	Status []domain.JobStatus
	Types  []string
	Search string
	// Sort is created_at (the backend's default) or updated_at.
	Sort string
	// Ascending flips the default newest-first order.
	Ascending bool
}

func (q ListQuery) values() url.Values {
	v := url.Values{}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.Offset > 0 {
		v.Set("offset", strconv.Itoa(q.Offset))
	}
	if len(q.Status) > 0 {
		s := make([]string, len(q.Status))
		for i, st := range q.Status {
			s[i] = string(st)
		}
		v.Set("status", strings.Join(s, ","))
	}
	if len(q.Types) > 0 {
		v.Set("type", strings.Join(q.Types, ","))
	}
	if q.Search != "" {
		v.Set("q", q.Search)
	}
	if q.Sort != "" {
		v.Set("sort", q.Sort)
	}
	if q.Ascending {
		v.Set("order", "asc")
	}
	return v
}

// Terminal reports whether a job status is final: completed, failed or
// cancelled.
func Terminal(s domain.JobStatus) bool {
	return s == domain.JobCompleted || s == domain.JobFailed || s == domain.JobCancelled
}

// Stats is the Stats schema of GET /api/v1/stats.
type Stats struct {
	GeneratedAt time.Time `json:"generated_at"`
	Days        int       `json:"days"`
	Totals      struct {
		Jobs            int   `json:"jobs"`
		TracksCompleted int   `json:"tracks_completed"`
		TracksSkipped   int   `json:"tracks_skipped"`
		TracksFailed    int   `json:"tracks_failed"`
		BytesWritten    int64 `json:"bytes_written"`
		DurationMS      int64 `json:"duration_ms"`
	} `json:"totals"`
	// Daily has one entry per UTC calendar day, oldest first, ending today.
	Daily []StatsDay `json:"daily"`
	Hooks []struct {
		Name         string     `json:"name"`
		Succeeded    int        `json:"succeeded"`
		Failed       int        `json:"failed"`
		LastFailedAt *time.Time `json:"last_failed_at,omitempty"`
	} `json:"hooks"`
}

// StatsDay is one day of Stats.daily.
type StatsDay struct {
	Date            string `json:"date"`
	TracksCompleted int    `json:"tracks_completed"`
	BytesWritten    int64  `json:"bytes_written"`
	JobsCompleted   int    `json:"jobs_completed"`
	JobsFailed      int    `json:"jobs_failed"`
}

// LibrarySync is the LibrarySyncStatus schema.
type LibrarySync struct {
	Enabled         bool       `json:"enabled"`
	IntervalMinutes int        `json:"interval_minutes"`
	AnchorSize      int        `json:"anchor_size"`
	LastPollAt      *time.Time `json:"last_poll_at,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
	LastSubmitted   int        `json:"last_submitted"`
	TotalSubmitted  int        `json:"total_submitted"`
}

// Hooks is the HooksResponse schema.
type Hooks struct {
	Enabled bool        `json:"enabled"`
	Entries []HookEntry `json:"entries"`
}

// HookEntry is the HookEntry schema. Method, Origin, HeaderNames and
// MaxAttempts are a webhook's; a command hook has none.
type HookEntry struct {
	Name        string   `json:"name"`
	Enabled     bool     `json:"enabled"`
	Type        string   `json:"type"`
	Events      []string `json:"events"`
	JobTypes    []string `json:"job_types"`
	Method      string   `json:"method,omitempty"`
	Origin      string   `json:"origin,omitempty"`
	HeaderNames []string `json:"header_names,omitempty"`
	MaxAttempts int      `json:"max_attempts,omitempty"`
}

// HookWrite is the WebhookWrite schema: a field left nil, or empty where it
// is not a pointer, keeps the entry's value.
type HookWrite struct {
	Enabled     *bool              `json:"enabled,omitempty"`
	Events      []string           `json:"events,omitempty"`
	JobTypes    *[]string          `json:"job_types,omitempty"`
	MaxAttempts *int               `json:"max_attempts,omitempty"`
	Method      string             `json:"method,omitempty"`
	URL         string             `json:"url,omitempty"`
	Headers     *map[string]string `json:"headers,omitempty"`
	Body        *string            `json:"body,omitempty"`
}

// HookTest is the HookTestResult schema.
type HookTest struct {
	Succeeded bool   `json:"succeeded"`
	Error     string `json:"error,omitempty"`
}
