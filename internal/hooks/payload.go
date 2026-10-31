package hooks

import (
	"time"

	"amdl/internal/domain"
)

type Payload struct {
	Event     string        `json:"event"`
	Timestamp string        `json:"timestamp"`
	Job       JobPayload    `json:"job"`
	Items     []ItemPayload `json:"items,omitempty"`
}

// JobPayload is the job as hooks see it. The counters have the meaning of the
// API's job counters (docs/architecture.md section 3.2): disjoint, and always
// present.
type JobPayload struct {
	ID         string `json:"id"`
	Input      string `json:"input"`
	Type       string `json:"type"`
	Storefront string `json:"storefront,omitempty"`
	// Title and ArtistName are the job's name and artist as the queue shows
	// them, absent until its link resolves (always for job_queued).
	Title          string     `json:"title,omitempty"`
	ArtistName     string     `json:"artist_name,omitempty"`
	Status         string     `json:"status"`
	TotalItems     int        `json:"total_items"`
	CompletedItems int        `json:"completed_items"`
	SkippedItems   int        `json:"skipped_items"`
	FailedItems    int        `json:"failed_items"`
	CancelledItems int        `json:"cancelled_items"`
	Error          string     `json:"error,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
}

// ItemPayload is one track as hooks see it. OutputPath is absolute.
type ItemPayload struct {
	ID         string `json:"id"`
	Title      string `json:"title,omitempty"`
	Artist     string `json:"artist,omitempty"`
	Album      string `json:"album,omitempty"`
	Status     string `json:"status"`
	Codec      string `json:"codec,omitempty"`
	OutputPath string `json:"output_path,omitempty"`
	Error      string `json:"error,omitempty"`
}

func jobPayload(job domain.Job) JobPayload {
	return JobPayload{
		ID: job.ID, Input: job.Input, Type: job.Type, Storefront: job.Storefront,
		Title: job.Title, ArtistName: job.ArtistName,
		Status: string(job.Status), TotalItems: job.TotalItems,
		CompletedItems: job.CompletedItems, SkippedItems: job.SkippedItems,
		FailedItems: job.FailedItems, CancelledItems: job.CancelledItems,
		Error: job.Error, StartedAt: job.StartedAt, FinishedAt: job.FinishedAt,
	}
}

func itemPayloads(items []domain.JobItem) []ItemPayload {
	out := make([]ItemPayload, 0, len(items))
	for _, item := range items {
		out = append(out, ItemPayload{
			ID: item.ID, Title: item.Title, Artist: item.Artist, Album: item.Album,
			Status: string(item.Status), Codec: item.Codec, OutputPath: domain.AbsolutePath(item.OutputPath), Error: item.Error,
		})
	}
	return out
}
