package media

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// A track that finds no checkpoint of its own leaves the job's checkpoint
// directory alone: a sibling track may just have created it and be about to
// open its checkpoint there.
func TestMissingCheckpointKeepsTheJobsDirectory(t *testing.T) {
	dir := t.TempDir()
	sibling, _ := resumableDownloadPaths(dir, testResumeOwner, "sibling.m4a")
	if err := os.MkdirAll(filepath.Dir(sibling), 0o700); err != nil {
		t.Fatal(err)
	}
	path, metadataPath := resumableDownloadPaths(dir, testResumeOwner, "track.m4a")
	if offset, _ := loadResumeCheckpoint(path, metadataPath, "https://example.com/track.mp4"); offset != 0 {
		t.Fatalf("offset = %d without a checkpoint, want 0", offset)
	}
	if _, err := os.Stat(filepath.Dir(sibling)); err != nil {
		t.Fatalf("the job's checkpoint directory is gone: %v", err)
	}
}

// Tracks of one job open checkpoints in the job's directory while others look
// for theirs or clean up after decrypting, and every open succeeds.
func TestConcurrentCheckpointsShareTheJobsDirectory(t *testing.T) {
	dir := t.TempDir()
	const rounds = 2000
	var wg sync.WaitGroup
	errs := make(chan error, rounds)
	wg.Go(func() {
		for i := range rounds {
			path, _ := resumableDownloadPaths(dir, testResumeOwner, "opener.m4a")
			sink := &fileSink{path: path}
			if err := sink.open(); err != nil {
				errs <- err
				continue
			}
			_ = sink.close()
			if i%2 == 0 {
				cleanupResumableDownload(path)
			}
		}
	})
	wg.Go(func() {
		for range rounds {
			path, metadataPath := resumableDownloadPaths(dir, testResumeOwner, "prober.m4a")
			loadResumeCheckpoint(path, metadataPath, "https://example.com/prober.mp4")
		}
	})
	wg.Wait()
	close(errs)
	failed := 0
	var first error
	for err := range errs {
		if first == nil {
			first = err
		}
		if errors.Is(err, fs.ErrNotExist) {
			failed++
		}
	}
	if first != nil {
		t.Fatalf("%d of %d checkpoint opens failed, first: %v", failed, rounds, first)
	}
}
