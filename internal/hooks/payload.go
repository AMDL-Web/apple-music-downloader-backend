package hooks

import "amdl/internal/domain"

type Payload struct {
	Event     string        `json:"event"`
	Timestamp string        `json:"timestamp"`
	Job       JobPayload    `json:"job"`
	Items     []ItemPayload `json:"items,omitempty"`
}

type JobPayload struct {
	ID          string `json:"id"`
	Input       string `json:"input"`
	Type        string `json:"type"`
	Storefront  string `json:"storefront,omitempty"`
	Status      string `json:"status"`
	TotalItems  int    `json:"total_items"`
	DoneItems   int    `json:"done_items"`
	FailedItems int    `json:"failed_items"`
	Error       string `json:"error,omitempty"`
}

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
		Status: string(job.Status), TotalItems: job.TotalItems, DoneItems: job.DoneItems,
		FailedItems: job.FailedItems, Error: job.Error,
	}
}

func itemPayloads(items []domain.JobItem) []ItemPayload {
	out := make([]ItemPayload, 0, len(items))
	for _, item := range items {
		out = append(out, ItemPayload{
			ID: item.ID, Title: item.Title, Artist: item.Artist, Album: item.Album,
			Status: string(item.Status), Codec: item.Codec, OutputPath: item.OutputPath, Error: item.Error,
		})
	}
	return out
}
