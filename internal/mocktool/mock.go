package mocktool

import (
	"context"
	"fmt"
	"strings"
	"time"

	"amdl/internal/applemusic"
	"amdl/internal/config"
	"amdl/internal/domain"
	"amdl/internal/jobs"
	"amdl/internal/media"
	"amdl/internal/storage"
	"amdl/internal/wrapper"
)

const defaultStorefronts = "us,cn,jp,gb,kr"

type Services struct {
	Processor *Processor
	Wrapper   *Wrapper
	Quality   *Quality
	Token     *DeveloperToken
}

func NewServices(cfg config.Config) *Services {
	regions := parseRegions(defaultStorefronts)
	return &Services{
		Processor: &Processor{cfg: cfg, regions: regions, stepDelay: 150 * time.Millisecond},
		Wrapper:   &Wrapper{regions: regions},
		Quality:   &Quality{cfg: cfg},
		Token:     &DeveloperToken{},
	}
}

type Processor struct {
	cfg       config.Config
	regions   []string
	stepDelay time.Duration
}

func (p *Processor) ValidateRequest(ctx context.Context, raw string) (jobs.ValidationResult, error) {
	parsed, err := applemusic.ParseWithAlbumTrackMode(strings.TrimSpace(raw), p.cfg.Catalog.AlbumTrackURLMode)
	if err != nil {
		return jobs.ValidationResult{}, &jobs.RequestError{Code: "invalid_url", Message: err.Error(), Cause: err}
	}
	if parsed.Type == applemusic.TypeVideo {
		return jobs.ValidationResult{}, &jobs.RequestError{Code: "unsupported_input", Message: "music-video download is not implemented"}
	}
	if !containsRegion(p.regions, parsed.Storefront) {
		return jobs.ValidationResult{}, &jobs.RequestError{Code: "unsupported_storefront", Message: fmt.Sprintf("storefront %q is not enabled in mock wrapper", parsed.Storefront), Storefront: parsed.Storefront, SupportedStorefronts: append([]string(nil), p.regions...)}
	}
	return jobs.ValidationResult{Type: string(parsed.Type), Storefront: parsed.Storefront, ID: parsed.ID}, nil
}

func (p *Processor) ProcessJob(ctx context.Context, job domain.Job, reporter jobs.Reporter) error {
	parsed, err := applemusic.ParseWithAlbumTrackMode(job.Input, p.cfg.Catalog.AlbumTrackURLMode)
	if err != nil {
		return err
	}
	count := mockTrackCount(parsed.Type)
	job.Type = string(parsed.Type)
	job.Storefront = parsed.Storefront
	job.Title = fmt.Sprintf("Mock %s %s", parsed.Type, parsed.ID)
	job.ArtworkURL = "https://example.invalid/mock-artwork.jpg"
	job.TotalItems = count
	if err := reporter.SetJob(ctx, job); err != nil {
		return err
	}
	if err := reporter.Event(ctx, domain.Event{JobID: job.ID, Type: "resolved_input", Message: job.Title}); err != nil {
		return err
	}
	for i := 1; i <= count; i++ {
		item := domain.JobItem{ID: storage.NewID("item"), JobID: job.ID, AdamID: fmt.Sprintf("%s-%02d", parsed.ID, i), Kind: "song", Index: i, Title: fmt.Sprintf("Mock Track %02d", i), Artist: "Mock Artist", Album: job.Title, ArtworkURL: job.ArtworkURL, Status: domain.ItemQueued}
		if err := reporter.AddItem(ctx, item); err != nil {
			return err
		}
		if err := p.runItem(ctx, reporter, &item); err != nil {
			return err
		}
	}
	return nil
}

func (p *Processor) runItem(ctx context.Context, reporter jobs.Reporter, item *domain.JobItem) error {
	steps := []struct {
		status     domain.ItemStatus
		event, msg string
		progress   float64
	}{
		{domain.ItemResolving, "item_resolving", "mock metadata resolved", 0.15},
		{domain.ItemDownloading, "item_progress", "mock download in progress", 0.45},
		{domain.ItemDecrypting, "item_progress", "mock decrypt in progress", 0.70},
		{domain.ItemRemuxing, "item_progress", "mock remux in progress", 0.90},
	}
	for _, step := range steps {
		select {
		case <-ctx.Done():
			item.Status = domain.ItemCancelled
			_ = reporter.UpdateItem(context.Background(), *item)
			return ctx.Err()
		case <-time.After(p.stepDelay):
		}
		item.Status, item.Progress, item.Codec, item.BitDepth, item.SampleRate, item.Bitrate, item.StatusMessage = step.status, step.progress, "alac", 24, 48000, 900000, step.msg
		if err := reporter.UpdateItem(ctx, *item); err != nil {
			return err
		}
		if err := reporter.Event(ctx, domain.Event{JobID: item.JobID, ItemID: item.ID, Type: step.event, Phase: string(step.status), Message: step.msg}); err != nil {
			return err
		}
	}
	item.Status, item.Progress, item.StatusMessage = domain.ItemCompleted, 1, "mock item completed"
	if err := reporter.UpdateItem(ctx, *item); err != nil {
		return err
	}
	return reporter.Event(ctx, domain.Event{JobID: item.JobID, ItemID: item.ID, Type: "item_completed", Message: item.StatusMessage})
}

func mockTrackCount(t applemusic.URLType) int {
	switch t {
	case applemusic.TypeAlbum:
		return 3
	case applemusic.TypePlaylist:
		return 4
	case applemusic.TypeArtist:
		return 5
	default:
		return 1
	}
}

type Wrapper struct{ regions []string }

func (w *Wrapper) Status(context.Context) (wrapper.Status, error) {
	return wrapper.Status{Ready: true, Status: true, Regions: append([]string(nil), w.regions...), ClientCount: 1, Accounts: []string{"mock@example.invalid"}, AccountsSupported: true}, nil
}
func (w *Wrapper) StartLogin(context.Context, string, string) (wrapper.LoginResult, error) {
	return wrapper.LoginResult{Status: wrapper.LoginStatusLoggedIn}, nil
}
func (w *Wrapper) SubmitTwoStepCode(context.Context, string, string) (wrapper.LoginResult, error) {
	return wrapper.LoginResult{Status: wrapper.LoginStatusLoggedIn}, nil
}
func (w *Wrapper) Logout(context.Context, string) error { return nil }

type Quality struct{ cfg config.Config }

func (q *Quality) QueryQuality(_ context.Context, req media.QualityRequest) (media.QualityResult, error) {
	parsed, err := applemusic.ParseWithAlbumTrackMode(strings.TrimSpace(req.URL), q.cfg.Catalog.AlbumTrackURLMode)
	if err != nil {
		return media.QualityResult{}, err
	}
	if parsed.Type != applemusic.TypeSong {
		return media.QualityResult{}, fmt.Errorf("quality query only supports song URLs")
	}
	return media.QualityResult{Input: req.URL, Storefront: parsed.Storefront, Type: string(parsed.Type), AdamID: parsed.ID, Song: media.QualitySong{ID: parsed.ID, Name: "Mock Track", Artist: "Mock Artist", Album: "Mock Album"}, Qualities: []media.QualityOption{{ID: "alac", Label: "Lossless", Available: true, Codec: "ALAC", Channels: 2, BitDepth: 24, SampleRate: 48000, Bitrate: 900000, Description: "ALAC | 2 Channel | 24-bit/48 kHz"}, {ID: "aac", Label: "AAC", Available: true, Codec: "AAC", Channels: 2, Bitrate: 256000, Description: "AAC | 2 Channel | 256 kbps"}}}, nil
}

type DeveloperToken struct{}

func (d *DeveloperToken) MintDeveloperToken() (string, error) { return "mock-developer-token", nil }

func parseRegions(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.ToLower(strings.TrimSpace(p)); s != "" {
			out = append(out, s)
		}
	}
	return out
}
func containsRegion(regions []string, want string) bool {
	for _, r := range regions {
		if strings.EqualFold(r, want) {
			return true
		}
	}
	return false
}
