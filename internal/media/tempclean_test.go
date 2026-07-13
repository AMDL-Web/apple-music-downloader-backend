package media

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupStaleTempRemovesOnlyDownloaderArtifacts(t *testing.T) {
	dir := t.TempDir()

	// Downloader scratch artifacts (files and ffmpeg working dirs) that a crash
	// could have left behind.
	stale := []string{"raw-abc.mp4", "dec-xyz.mp4", "flat-1.m4a"}
	staleDirs := []string{"fix-9", "check-2"}
	// Unrelated content that must survive.
	keep := []string{"keep.txt", "config.yaml", "MyAlbum.m4a"}

	for _, name := range append(append([]string{}, stale...), keep...) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range staleDirs {
		if err := os.MkdirAll(filepath.Join(dir, name, "inner"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	CleanupStaleTemp(dir, nil)

	for _, name := range append(append([]string{}, stale...), staleDirs...) {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("stale artifact %q not removed (err=%v)", name, err)
		}
	}
	for _, name := range keep {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("unrelated file %q was removed: %v", name, err)
		}
	}
}

func TestCleanupStaleTempMissingDirIsNoop(t *testing.T) {
	// Must not panic or error when the temp dir doesn't exist yet.
	CleanupStaleTemp(filepath.Join(t.TempDir(), "does-not-exist"), nil)
	CleanupStaleTemp("", nil)
}
