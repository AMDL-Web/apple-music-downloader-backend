package domain

import "time"

type JobStatus string

const (
	JobQueued    JobStatus = "queued"
	JobRunning   JobStatus = "running"
	JobCompleted JobStatus = "completed"
	JobFailed    JobStatus = "failed"
	JobCancelled JobStatus = "cancelled"
)

// IsTerminal reports whether a job in this status will never emit another
// event: no worker is running and none will be scheduled.
func (s JobStatus) IsTerminal() bool {
	switch s {
	case JobCompleted, JobFailed, JobCancelled:
		return true
	default:
		return false
	}
}

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
	ID           string    `json:"id"`
	Input        string    `json:"input"`
	Type         string    `json:"type"`
	Storefront   string    `json:"storefront,omitempty"`
	Title        string    `json:"title,omitempty"`
	ArtworkURL   string    `json:"artwork_url,omitempty"`
	CanonicalKey string    `json:"-"`
	Force        bool      `json:"force"`
	Status       JobStatus `json:"status"`
	TotalItems   int       `json:"total_items"`
	DoneItems    int       `json:"done_items"`
	FailedItems  int       `json:"failed_items"`
	Error        string    `json:"error,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
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
	ArtworkURL    string     `json:"artwork_url,omitempty"`
	Status        ItemStatus `json:"status"`
	Progress      float64    `json:"progress"`
	Codec         string     `json:"codec,omitempty"`
	RetryKind     string     `json:"retry_kind,omitempty"`
	Attempt       int        `json:"attempt,omitempty"`
	MaxAttempts   int        `json:"max_attempts,omitempty"`
	StatusMessage string     `json:"status_message,omitempty"`
	OutputPath    string     `json:"-"`
	Error         string     `json:"error,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// CountItemProgress reports how many items in the slice are finished (completed
// or skipped) versus failed, using the same done/failed accounting applied when
// a job's DoneItems/FailedItems counters are refreshed. Deriving the counters
// from the live item list keeps a job's reported progress consistent with the
// items returned alongside it, even before the job reaches a terminal status.
func CountItemProgress(items []JobItem) (done, failed int) {
	for _, item := range items {
		switch item.Status {
		case ItemFailed:
			failed++
		case ItemCompleted, ItemSkipped:
			done++
		}
	}
	return done, failed
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
