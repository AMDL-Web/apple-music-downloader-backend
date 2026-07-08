package mocktool

import (
	"context"
	"testing"

	"amdl/internal/config"
	"amdl/internal/domain"
	"amdl/internal/jobs"
	"amdl/internal/media"
)

type recordingReporter struct {
	job    domain.Job
	items  []domain.JobItem
	events []domain.Event
}

func (r *recordingReporter) SetJob(_ context.Context, job domain.Job) error { r.job = job; return nil }
func (r *recordingReporter) AddItem(_ context.Context, item domain.JobItem) error {
	r.items = append(r.items, item)
	return nil
}
func (r *recordingReporter) UpdateItem(_ context.Context, item domain.JobItem) error {
	for i := range r.items {
		if r.items[i].ID == item.ID {
			r.items[i] = item
			return nil
		}
	}
	r.items = append(r.items, item)
	return nil
}
func (r *recordingReporter) Event(_ context.Context, ev domain.Event) error {
	r.events = append(r.events, ev)
	return nil
}

func TestProcessorValidateRequest(t *testing.T) {
	processor := NewServices(config.Default()).Processor
	got, err := processor.ValidateRequest(context.Background(), "https://music.apple.com/us/album/example/123?i=456")
	if err != nil {
		t.Fatalf("ValidateRequest returned error: %v", err)
	}
	if got.Type != "song" || got.Storefront != "us" || got.ID != "456" {
		t.Fatalf("unexpected validation result: %+v", got)
	}
	_, err = processor.ValidateRequest(context.Background(), "https://music.apple.com/fr/song/example/123")
	if err == nil {
		t.Fatal("expected unsupported storefront error")
	}
	if reqErr, ok := err.(*jobs.RequestError); !ok || reqErr.Code != "unsupported_storefront" {
		t.Fatalf("unexpected error: %#v", err)
	}
}

func TestProcessorProcessJobCompletesMockItems(t *testing.T) {
	services := NewServices(config.Default())
	services.Processor.stepDelay = 0
	reporter := &recordingReporter{}
	job := domain.Job{ID: "job_mock", Input: "https://music.apple.com/us/album/example/123", Type: "album", Storefront: "us"}
	if err := services.Processor.ProcessJob(context.Background(), job, reporter); err != nil {
		t.Fatalf("ProcessJob returned error: %v", err)
	}
	if reporter.job.TotalItems != 3 {
		t.Fatalf("TotalItems = %d, want 3", reporter.job.TotalItems)
	}
	if len(reporter.items) != 3 {
		t.Fatalf("items = %d, want 3", len(reporter.items))
	}
	for _, item := range reporter.items {
		if item.Status != domain.ItemCompleted || item.Progress != 1 {
			t.Fatalf("item not completed: %+v", item)
		}
	}
}

func TestQualityAndWrapperMocks(t *testing.T) {
	services := NewServices(config.Default())
	status, err := services.Wrapper.Status(context.Background())
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if !status.Ready || len(status.Regions) == 0 {
		t.Fatalf("unexpected status: %+v", status)
	}
	quality, err := services.Quality.QueryQuality(context.Background(), media.QualityRequest{URL: "https://music.apple.com/us/song/example/123"})
	if err != nil {
		t.Fatalf("QueryQuality returned error: %v", err)
	}
	if quality.AdamID != "123" || len(quality.Qualities) == 0 {
		t.Fatalf("unexpected quality result: %+v", quality)
	}
}
