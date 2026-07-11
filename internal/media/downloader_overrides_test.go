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

// TestProcessJobHonorsCanonicalKeyOverReparse pins execution to the parse
// result recorded at submission: an album?i= link validated as a song (mode
// "song") must still process as that song even if
// catalog.album_track_url_mode flipped to "album" while the job was queued —
// re-parsing under the new mode would target the whole album and diverge
// from the job's dedup key and metadata.
func TestProcessJobHonorsCanonicalKeyOverReparse(t *testing.T) {
	cfg := config.Default()
	cfg.Simulate = config.SimulateConfig{Enabled: true, MinSpeedKBps: 1_000_000, MaxSpeedKBps: 1_000_000}
	cfg.Download.DownloadsDir = t.TempDir()
	cfg.Download.MaxAttempts = 1
	cfg.Catalog.AlbumTrackURLMode = "album" // changed after the job was submitted

	song := applemusic.Song{ID: "987654321", Name: "Track", ArtistName: "Artist", AlbumName: "Album", DurationInMillis: 1000}
	downloader := &Downloader{
		store: config.NewStore(cfg),
		// Only the song fake is populated: if processJob re-parsed the input
		// under the current "album" mode it would resolve via catalog.Album
		// (empty) and fail with "no downloadable songs found".
		catalog: fakeDownloaderCatalog{song: song},
	}
	reporter := &recordingReporter{}
	job := domain.Job{
		ID:           "job-1",
		Input:        "https://music.apple.com/us/album/foo/123456789?i=987654321",
		CanonicalKey: "song:us:987654321",
	}
	if err := downloader.ProcessJob(context.Background(), job, reporter); err != nil {
		t.Fatalf("ProcessJob failed (job re-parsed under current mode instead of using its canonical key?): %v", err)
	}
	final := reporter.items[len(reporter.items)-1]
	if final.Status != domain.ItemCompleted || final.AdamID != "987654321" {
		t.Fatalf("final item = %s/%s, want completed song 987654321", final.Status, final.AdamID)
	}
}
