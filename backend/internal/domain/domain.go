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
	ID          string    `json:"id"`
	Input       string    `json:"input"`
	Type        string    `json:"type"`
	Storefront  string    `json:"storefront,omitempty"`
	Status      JobStatus `json:"status"`
	TotalItems  int       `json:"total_items"`
	DoneItems   int       `json:"done_items"`
	FailedItems int       `json:"failed_items"`
	Error       string    `json:"error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type JobItem struct {
	ID         string     `json:"id"`
	JobID      string     `json:"job_id"`
	AdamID     string     `json:"adam_id"`
	Kind       string     `json:"kind"`
	Index      int        `json:"index"`
	Title      string     `json:"title,omitempty"`
	Artist     string     `json:"artist,omitempty"`
	Album      string     `json:"album,omitempty"`
	Status     ItemStatus `json:"status"`
	Progress   float64    `json:"progress"`
	Codec      string     `json:"codec,omitempty"`
	OutputPath string     `json:"output_path,omitempty"`
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
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
	URL   string `json:"url"`
	Force bool   `json:"force"`
}

type Capability struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Path      string `json:"path,omitempty"`
	Error     string `json:"error,omitempty"`
}
