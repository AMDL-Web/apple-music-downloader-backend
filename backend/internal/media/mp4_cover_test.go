package media

import (
	"os"
	"path/filepath"
	"testing"

	"amdl/backend/internal/config"
)

func TestTemporaryEmbeddedCoverDoesNotTouchStandaloneCover(t *testing.T) {
	root := t.TempDir()
	outputDir := filepath.Join(root, "downloads", "playlists", "List")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	standalonePath := filepath.Join(outputDir, "cover.jpg")
	if err := os.WriteFile(standalonePath, []byte("playlist-cover"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{}
	cfg.Download.TempDir = filepath.Join(root, "tmp")
	cfg.Download.CoverFormat = "jpg"
	processor := newMP4Processor(cfg)
	temporaryPath, err := processor.writeTemporaryCover([]byte("track-cover"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(temporaryPath) })

	if filepath.Dir(temporaryPath) != cfg.Download.TempDir {
		t.Fatalf("temporary cover directory = %q, want %q", filepath.Dir(temporaryPath), cfg.Download.TempDir)
	}
	got, err := os.ReadFile(standalonePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "playlist-cover" {
		t.Fatalf("standalone cover changed to %q", got)
	}
}
