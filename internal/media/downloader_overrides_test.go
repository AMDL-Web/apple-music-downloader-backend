package media

import (
	"context"
	"strings"
	"testing"

	"amdl/internal/applemusic"
	"amdl/internal/config"
	"amdl/internal/domain"
)

// TestProcessJobAppliesPerJobOverrides runs two jobs through the same
// Downloader in simulate mode: one carrying a song_path_format override and
// one without. The overridden job's output path must follow the override and
// the plain job must keep the runtime config's format, proving the overlay is
// scoped to a single job and never leaks into the shared Downloader.
func TestProcessJobAppliesPerJobOverrides(t *testing.T) {
	cfg := config.Default()
	cfg.Simulate = config.SimulateConfig{Enabled: true, MinSpeedKBps: 1_000_000, MaxSpeedKBps: 1_000_000}
	cfg.Download.DownloadsDir = t.TempDir()
	cfg.Download.MaxAttempts = 1

	song := applemusic.Song{ID: "987654321", Name: "Track", ArtistName: "Artist", AlbumName: "Album", DurationInMillis: 1000}
	downloader := &Downloader{
		store:   config.NewStore(cfg),
		catalog: fakeDownloaderCatalog{song: song},
	}

	overrideFormat := "override-dir/{SongName}"
	runJob := func(t *testing.T, overrides *config.DownloadOverrides) domain.JobItem {
		t.Helper()
		reporter := &recordingReporter{}
		job := domain.Job{ID: "job-1", Input: "https://music.apple.com/us/song/foo/987654321", Overrides: overrides}
		if err := downloader.ProcessJob(context.Background(), job, reporter); err != nil {
			t.Fatalf("ProcessJob failed: %v", err)
		}
		if len(reporter.items) == 0 {
			t.Fatal("no item updates recorded")
		}
		final := reporter.items[len(reporter.items)-1]
		if final.Status != domain.ItemCompleted {
			t.Fatalf("final item status = %s, want completed (%+v)", final.Status, final)
		}
		return final
	}

	overridden := runJob(t, &config.DownloadOverrides{SongPathFormat: &overrideFormat})
	if !strings.Contains(overridden.OutputPath, "override-dir/Track") {
		t.Fatalf("overridden output path = %q, want it to follow song_path_format override", overridden.OutputPath)
	}

	plain := runJob(t, nil)
	if strings.Contains(plain.OutputPath, "override-dir") {
		t.Fatalf("plain job output path = %q leaked the previous job's override", plain.OutputPath)
	}
	if !strings.Contains(plain.OutputPath, "songs/Artist") {
		t.Fatalf("plain job output path = %q, want the runtime config's song_path_format", plain.OutputPath)
	}
}
