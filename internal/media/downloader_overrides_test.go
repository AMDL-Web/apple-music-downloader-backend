package media

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"amdl/internal/applemusic"
	"amdl/internal/config"
	"amdl/internal/domain"
)

func TestCleanupJobArtifactsUsesJobTempOverride(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.Download.TempDir = filepath.Join(root, "temp")
	overrideDir := filepath.Join(cfg.Download.TempDir, "job-scratch")
	downloader := &Downloader{store: config.NewStore(cfg)}
	job := domain.Job{ID: "job-clean", Overrides: &config.DownloadOverrides{TempDir: &overrideDir}}
	path, metadataPath := resumableDownloadPaths(overrideDir, job.ID, "output")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{path, metadataPath} {
		if err := os.WriteFile(candidate, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	downloader.CleanupJobArtifacts(job)
	if _, err := os.Stat(resumeOwnerDir(overrideDir, job.ID)); !os.IsNotExist(err) {
		t.Fatalf("job resume directory still exists: %v", err)
	}
}

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

func TestProcessJobRejectsPersistedOverrideOutsideConfiguredRoots(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Simulate = config.SimulateConfig{Enabled: true, MinSpeedKBps: 1_000_000, MaxSpeedKBps: 1_000_000}
	cfg.Download.DownloadsDir = filepath.Join(dir, "downloads")
	cfg.Download.TempDir = filepath.Join(dir, "temp")
	downloader := &Downloader{store: config.NewStore(cfg)}
	escaped := filepath.Join(dir, "outside")
	err := downloader.ProcessJob(context.Background(), domain.Job{
		ID: "job-unsafe", Overrides: &config.DownloadOverrides{DownloadsDir: &escaped},
	}, &recordingReporter{})
	if err == nil || !strings.Contains(err.Error(), "downloads_dir") {
		t.Fatalf("ProcessJob error = %v, want unsafe downloads_dir rejection", err)
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

// TestParseJobInputAcceptsBothKeyGenerations guards the failure mode that
// folding the destination into the canonical key creates here: this is the
// site that decides which substring of the key is the adam id, and reading
// the wrong substring produces no error at all — just a download of the wrong
// thing. Both the four-segment key written now and the three-segment key
// still sitting in deployed databases must yield the same adam id.
func TestParseJobInputAcceptsBothKeyGenerations(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
	}{
		{name: "with destination", key: "song:us:987654321:0123456789ab"},
		{name: "legacy without destination", key: "song:us:987654321"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := domain.Job{
				Input:        "https://music.apple.com/us/album/foo/123456789?i=987654321",
				CanonicalKey: tc.key,
			}
			// "album" is deliberately the opposite of what this key records,
			// so a fall-through to re-parsing would resolve the album id
			// 123456789 instead and be visible here.
			parsed, err := parseJobInput(job, "album")
			if err != nil {
				t.Fatalf("parseJobInput(%q): %v", tc.key, err)
			}
			if parsed.Type != applemusic.TypeSong || parsed.Storefront != "us" || parsed.ID != "987654321" {
				t.Fatalf("parseJobInput(%q) = %s/%s/%s, want song/us/987654321",
					tc.key, parsed.Type, parsed.Storefront, parsed.ID)
			}
		})
	}
}

// TestParseJobInputFallsBackForUnusableKey keeps the documented escape hatch:
// a key that cannot be read still re-parses the raw input rather than failing
// the job.
func TestParseJobInputFallsBackForUnusableKey(t *testing.T) {
	job := domain.Job{
		Input:        "https://music.apple.com/us/album/foo/123456789",
		CanonicalKey: "not-a-key",
	}
	parsed, err := parseJobInput(job, "song")
	if err != nil {
		t.Fatalf("parseJobInput: %v", err)
	}
	if parsed.Type != applemusic.TypeAlbum || parsed.ID != "123456789" {
		t.Fatalf("parseJobInput = %s/%s, want album/123456789 from the raw input", parsed.Type, parsed.ID)
	}
}
