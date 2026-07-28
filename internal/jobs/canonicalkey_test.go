package jobs

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"amdl/internal/config"
	"amdl/internal/db"
	"amdl/internal/domain"
	"amdl/internal/events"
)

// newDestinationManager is newTestManager plus a runtime config rooted at a
// real temp directory, so destination canonicalization has an existing path
// to resolve.
func newDestinationManager(t *testing.T, downloadsDir string) *Manager {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	m := NewManager(store, events.NewHub(), keyedProcessor{}, 1, slog.Default())
	m.SetConfigStore(config.NewStore(config.Config{
		Download: config.DownloadConfig{DownloadsDir: downloadsDir},
	}))
	return m
}

func dirOverride(dir string) *config.DownloadOverrides {
	return &config.DownloadOverrides{DownloadsDir: &dir}
}

// TestSubmitBatchAcceptsSameAlbumToDifferentDestinations is the whole point of
// folding the destination into the key: two jobs writing to different
// directories are not duplicates of each other, so the second must be accepted
// while the first is still active.
func TestSubmitBatchAcceptsSameAlbumToDifferentDestinations(t *testing.T) {
	root := t.TempDir()
	manager := newDestinationManager(t, root)
	ctx := context.Background()

	first := manager.SubmitBatch(ctx, []string{"album|us|111"}, dirOverride(filepath.Join(root, "music")))
	if first.Accepted != 1 {
		t.Fatalf("first submit = %+v, want 1 accepted", first.Results)
	}
	second := manager.SubmitBatch(ctx, []string{"album|us|111"}, dirOverride(filepath.Join(root, "backup")))
	if second.Accepted != 1 {
		t.Fatalf("second submit = %+v, want 1 accepted while the first is still queued", second.Results)
	}

	firstJob, secondJob := first.Results[0].Job, second.Results[0].Job
	if firstJob.ID == secondJob.ID {
		t.Fatalf("both submits returned job %s, want two distinct jobs", firstJob.ID)
	}
	if firstJob.CanonicalKey == secondJob.CanonicalKey {
		t.Fatalf("canonical keys collide: %q", firstJob.CanonicalKey)
	}
	// The first three segments still describe the same album, so log lines and
	// operator habits are unaffected; only the destination differs.
	for _, job := range []*domain.Job{firstJob, secondJob} {
		jobType, storefront, id, ok := ParseCanonicalKey(job.CanonicalKey)
		if !ok || jobType != "album" || storefront != "us" || id != "111" {
			t.Fatalf("ParseCanonicalKey(%q) = %q/%q/%q ok=%v, want album/us/111", job.CanonicalKey, jobType, storefront, id, ok)
		}
	}
}

// TestSubmitBatchRejectsDuplicateToSameDestination pins that dedup is not
// weakened: same album, same place, still refused with the existing job id.
func TestSubmitBatchRejectsDuplicateToSameDestination(t *testing.T) {
	root := t.TempDir()
	manager := newDestinationManager(t, root)
	ctx := context.Background()
	dest := filepath.Join(root, "music")

	first := manager.SubmitBatch(ctx, []string{"album|us|111"}, dirOverride(dest))
	if first.Accepted != 1 {
		t.Fatalf("first submit = %+v, want 1 accepted", first.Results)
	}
	second := manager.SubmitBatch(ctx, []string{"album|us|111"}, dirOverride(dest))
	if second.Results[0].Status != domain.SubmitDuplicateActive {
		t.Fatalf("second submit = %+v, want duplicate_active", second.Results[0])
	}
	if second.Results[0].ExistingJobID != first.Results[0].Job.ID {
		t.Fatalf("existing_job_id = %q, want %q", second.Results[0].ExistingJobID, first.Results[0].Job.ID)
	}
}

// TestSubmitBatchDedupesEquivalentDestinationSpellings is the canonicalization
// test: the backend accepts all of these spellings for one directory, so
// hashing the raw string would let a caller defeat their own dedup just by
// respelling the path.
func TestSubmitBatchDedupesEquivalentDestinationSpellings(t *testing.T) {
	root := t.TempDir()
	canonical := filepath.Join(root, "users", "a")
	// Create it so canonicalization resolves a real directory (on macOS the
	// temp root itself is reached through a symlink), rather than only
	// exercising the missing-tail branch.
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, spelling := range []string{
		canonical + string(filepath.Separator),           // trailing slash
		filepath.Join(root, ".", "users", "a"),           // "." segment
		root + string(filepath.Separator) + "./users/a/", // "." segment and trailing slash, unjoined
		root + string(filepath.Separator) + "users/b/../a",
	} {
		t.Run(spelling, func(t *testing.T) {
			manager := newDestinationManager(t, root)
			ctx := context.Background()

			first := manager.SubmitBatch(ctx, []string{"album|us|111"}, dirOverride(canonical))
			if first.Accepted != 1 {
				t.Fatalf("first submit = %+v, want 1 accepted", first.Results)
			}
			second := manager.SubmitBatch(ctx, []string{"album|us|111"}, dirOverride(spelling))
			if second.Results[0].Status != domain.SubmitDuplicateActive {
				t.Fatalf("submit to %q = %+v, want duplicate_active against %q", spelling, second.Results[0], canonical)
			}
		})
	}
}

// TestSubmitBatchDedupesImplicitAgainstExplicitDefault covers the submission
// with no downloads_dir override at all: it resolves to the config default, so
// it is the same place as an explicit override naming that default.
func TestSubmitBatchDedupesImplicitAgainstExplicitDefault(t *testing.T) {
	root := t.TempDir()
	manager := newDestinationManager(t, root)
	ctx := context.Background()

	first := manager.SubmitBatch(ctx, []string{"album|us|111"}, nil)
	if first.Accepted != 1 {
		t.Fatalf("first submit = %+v, want 1 accepted", first.Results)
	}
	// Spelled differently as well, to prove the two paths meet after
	// canonicalization rather than by string identity.
	second := manager.SubmitBatch(ctx, []string{"album|us|111"}, dirOverride(root+string(filepath.Separator)+"."))
	if second.Results[0].Status != domain.SubmitDuplicateActive {
		t.Fatalf("explicit-default submit = %+v, want duplicate_active", second.Results[0])
	}
	if second.Results[0].ExistingJobID != first.Results[0].Job.ID {
		t.Fatalf("existing_job_id = %q, want %q", second.Results[0].ExistingJobID, first.Results[0].Job.ID)
	}
}

func TestParseCanonicalKey(t *testing.T) {
	root := t.TempDir()
	built := canonicalKey("album", "us", "111", destinationSegment(
		config.Config{Download: config.DownloadConfig{DownloadsDir: root}}, nil))

	for _, tc := range []struct {
		name                        string
		key                         string
		wantOK                      bool
		wantType, wantStore, wantID string
	}{
		{name: "built key", key: built, wantOK: true, wantType: "album", wantStore: "us", wantID: "111"},
		// Backward compatibility: rows written before destinations joined the
		// key are still in the jobs table and must resolve unchanged, which is
		// what makes this a no-migration change.
		{name: "legacy three segment", key: "song:us:1440857781", wantOK: true, wantType: "song", wantStore: "us", wantID: "1440857781"},
		{name: "legacy station id", key: "station:us:ra.985484166", wantOK: true, wantType: "station", wantStore: "us", wantID: "ra.985484166"},
		{name: "legacy playlist id", key: "playlist:us:pl.u-abcdef", wantOK: true, wantType: "playlist", wantStore: "us", wantID: "pl.u-abcdef"},
		// A twelve-digit adam id is valid hex. Without the "something must be
		// left for the id" guard this would be mistaken for a destination.
		{name: "legacy hex-shaped id", key: "song:us:123456789012", wantOK: true, wantType: "song", wantStore: "us", wantID: "123456789012"},
		// ':' is legal in a URL path segment and nothing rejects it, so an id
		// containing one must survive both directions intact.
		{name: "id containing a colon", key: canonicalKey("song", "us", "od:d", "0123456789ab"), wantOK: true, wantType: "song", wantStore: "us", wantID: "od:d"},
		{name: "legacy id containing a colon", key: "song:us:od:d", wantOK: true, wantType: "song", wantStore: "us", wantID: "od:d"},
		{name: "too few segments", key: "song:us", wantOK: false},
		{name: "empty", key: "", wantOK: false},
		{name: "empty storefront", key: "song::111", wantOK: false},
		{name: "empty id", key: "song:us:", wantOK: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jobType, storefront, id, ok := ParseCanonicalKey(tc.key)
			if ok != tc.wantOK {
				t.Fatalf("ParseCanonicalKey(%q) ok = %v, want %v", tc.key, ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if jobType != tc.wantType || storefront != tc.wantStore || id != tc.wantID {
				t.Fatalf("ParseCanonicalKey(%q) = %q/%q/%q, want %q/%q/%q",
					tc.key, jobType, storefront, id, tc.wantType, tc.wantStore, tc.wantID)
			}
		})
	}
}

// TestDestinationSegmentShape pins the format the parser's suffix detection
// depends on: a fixed-width lowercase hex string, stable for one directory and
// different for another.
func TestDestinationSegmentShape(t *testing.T) {
	root := t.TempDir()
	base := config.Config{Download: config.DownloadConfig{DownloadsDir: root}}

	seg := destinationSegment(base, nil)
	if !isDestinationSegment(seg) {
		t.Fatalf("destinationSegment = %q, which the parser would not recognize", seg)
	}
	if again := destinationSegment(base, nil); again != seg {
		t.Fatalf("destinationSegment is not stable: %q then %q", seg, again)
	}
	if other := destinationSegment(base, dirOverride(filepath.Join(root, "elsewhere"))); other == seg {
		t.Fatalf("a different directory produced the same segment %q", seg)
	}
	// The raw path must not leak into the key: it belongs neither in a DB
	// index nor anywhere a ':' in a directory name could reach the parser.
	if len(seg) != destinationSegmentLen {
		t.Fatalf("destinationSegment %q is %d chars, want %d", seg, len(seg), destinationSegmentLen)
	}
}
