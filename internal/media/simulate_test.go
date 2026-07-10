package media

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"amdl/internal/applemusic"
	"amdl/internal/config"
	"amdl/internal/domain"
)

func TestSimulateTrackSelectsRealMediaButNeverDownloadsOrWrites(t *testing.T) {
	var encryptedMediaHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/master.m3u8":
			_, _ = w.Write([]byte("#EXTM3U\n" +
				"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"audio-alac-stereo-96000-24\",NAME=\"Lossless\",BIT-DEPTH=24,SAMPLE-RATE=96000\n" +
				"#EXT-X-STREAM-INF:BANDWIDTH=3000000,AVERAGE-BANDWIDTH=2500000,AUDIO=\"audio-alac-stereo-96000-24\",CODECS=\"alac\"\n" +
				"media.m3u8\n"))
		case "/media.m3u8":
			_, _ = w.Write([]byte("#EXTM3U\n" +
				"#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"skd://itunes.apple.com/P000000000/s1/e1/c23\"\n" +
				"#EXT-X-MAP:URI=\"encrypted.mp4\"\n"))
		case "/encrypted.mp4":
			encryptedMediaHits.Add(1)
			_, _ = w.Write([]byte("encrypted media bytes"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Simulate = config.SimulateConfig{Enabled: true, MinSpeedKBps: 1_000_000, MaxSpeedKBps: 1_000_000}
	cfg.Download.DownloadsDir = t.TempDir()
	song := applemusic.Song{
		ID: "song-1", Name: "Track", ArtistName: "Artist", AlbumName: "Album",
		DurationInMillis: 1000, EnhancedHLS: server.URL + "/master.m3u8",
	}
	downloader := &Downloader{
		cfg:     cfg,
		catalog: fakeDownloaderCatalog{song: song},
		http:    server.Client(),
	}
	reporter := &recordingReporter{}
	job := domain.Job{ID: "job-1"}
	item := domain.JobItem{ID: "item-1", JobID: "job-1"}

	if err := downloader.processTrack(context.Background(), job, item, applemusic.Song{ID: "song-1"}, "cn", applemusic.TypeAlbum, "Album", "album-1", 1, "", reporter); err != nil {
		t.Fatalf("simulated processTrack failed: %v", err)
	}
	if encryptedMediaHits.Load() != 0 {
		t.Fatalf("simulate mode downloaded encrypted media %d time(s), want 0", encryptedMediaHits.Load())
	}

	seenStatuses := map[string]bool{}
	var codecSelected, itemCompleted bool
	for _, ev := range reporter.events {
		switch ev.Type {
		case "item_progress":
			seenStatuses[ev.Phase] = true
		case "codec_selected":
			codecSelected = true
			if ev.Phase != "alac" {
				t.Fatalf("codec_selected phase = %q, want alac (first quality_priority codec)", ev.Phase)
			}
		case "item_completed":
			itemCompleted = true
		case "item_failed", "item_skipped", "codec_failed", "codec_fallback":
			t.Fatalf("unexpected %s event in simulated happy path: %+v", ev.Type, ev)
		}
	}
	for _, status := range []domain.ItemStatus{
		domain.ItemResolving, domain.ItemDownloading, domain.ItemDecrypting,
		domain.ItemRemuxing, domain.ItemSaving, domain.ItemTagging,
	} {
		if !seenStatuses[string(status)] {
			t.Errorf("missing item_progress event for status %q", status)
		}
	}
	if !codecSelected {
		t.Error("missing codec_selected event")
	}
	if !itemCompleted {
		t.Error("missing item_completed event")
	}

	if len(reporter.items) == 0 {
		t.Fatal("expected item updates")
	}
	final := reporter.items[len(reporter.items)-1]
	if final.Status != domain.ItemCompleted || final.Progress != 1 {
		t.Fatalf("final item state = %s/%v, want completed/1", final.Status, final.Progress)
	}
	if final.Codec != "alac" || final.OutputPath == "" {
		t.Fatalf("final item codec/output_path = %q/%q, want alac and a non-empty path", final.Codec, final.OutputPath)
	}
	if final.BitDepth != 24 || final.SampleRate != 96000 || final.Bitrate != 2500000 {
		t.Fatalf("final item quality = %d/%d/%d, want the manifest's real 24/96000/2500000", final.BitDepth, final.SampleRate, final.Bitrate)
	}

	_ = filepath.WalkDir(cfg.Download.DownloadsDir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			t.Errorf("simulate mode wrote a file to disk: %s", path)
		}
		return nil
	})
}

func TestSimulateTrackFallsBackToAACLCWithoutManifest(t *testing.T) {
	cfg := config.Default()
	cfg.Simulate = config.SimulateConfig{Enabled: true, MinSpeedKBps: 1_000_000, MaxSpeedKBps: 1_000_000}
	cfg.Download.DownloadsDir = t.TempDir()
	// No EnhancedHLS manifest: every enhanced codec fails selection and the
	// simulated run must walk the same fallback chain down to AAC-LC.
	downloader := &Downloader{
		cfg:     cfg,
		catalog: fakeDownloaderCatalog{song: applemusic.Song{ID: "song-1", Name: "Track", DurationInMillis: 1000}},
	}
	reporter := &recordingReporter{}

	if err := downloader.processTrack(context.Background(), domain.Job{ID: "job-1"}, domain.JobItem{ID: "item-1", JobID: "job-1"}, applemusic.Song{ID: "song-1"}, "cn", applemusic.TypeAlbum, "Album", "album-1", 1, "", reporter); err != nil {
		t.Fatalf("simulated processTrack failed: %v", err)
	}
	var fallbacks, completed int
	for _, ev := range reporter.events {
		switch ev.Type {
		case "codec_fallback":
			fallbacks++
		case "item_completed":
			completed++
		}
	}
	if fallbacks == 0 {
		t.Error("expected codec_fallback events when the enhanced manifest is missing")
	}
	if completed != 1 {
		t.Fatalf("item_completed events = %d, want 1", completed)
	}
	final := reporter.items[len(reporter.items)-1]
	if final.Codec != "aac-lc" || final.Status != domain.ItemCompleted {
		t.Fatalf("final item codec/status = %q/%s, want aac-lc/completed", final.Codec, final.Status)
	}
}

func TestSimulateModeSkipsWrapperStorefrontCheck(t *testing.T) {
	cfg := config.Default()
	cfg.Simulate.Enabled = true
	downloader := &Downloader{cfg: cfg} // no wrapper wired at all
	if err := downloader.validateStorefront(context.Background(), "cn"); err != nil {
		t.Fatalf("simulate mode must not require a running wrapper, got: %v", err)
	}
}
