package db

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"amdl/internal/config"
	"amdl/internal/domain"
)

func TestGetJobFailsOnCorruptOverrides(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()

	embed := false
	now := time.Now().UTC()
	job := domain.Job{
		ID: "job-1", Input: "song|us|1", Type: "song", CanonicalKey: "song:us:1",
		Overrides: &config.DownloadOverrides{EmbedLyrics: &embed},
		Status:    domain.JobQueued, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetJob(ctx, job.ID)
	if err != nil || loaded.Overrides == nil || loaded.Overrides.EmbedLyrics == nil || *loaded.Overrides.EmbedLyrics {
		t.Fatalf("healthy round trip failed: %+v, %v", loaded.Overrides, err)
	}

	// Corrupt the column behind the store's back: the load must fail loudly
	// instead of silently running the job with the global config.
	if _, err := store.db.ExecContext(ctx, `UPDATE jobs SET overrides='{broken' WHERE id=?`, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetJob(ctx, job.ID); err == nil || !strings.Contains(err.Error(), "decode overrides") {
		t.Fatalf("corrupt overrides load err = %v, want decode overrides error", err)
	}
}

func TestMediaUserTokenOverrideSurvivesStorageRoundTrip(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	empty := ""
	now := time.Now().UTC()
	job := domain.Job{
		ID: "job-token", Input: "station|us|ra.1", Type: "station", CanonicalKey: "station:us:ra.1",
		Overrides: &config.DownloadOverrides{MediaUserToken: &empty},
		Status:    domain.JobQueued, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Overrides == nil || loaded.Overrides.MediaUserToken == nil || *loaded.Overrides.MediaUserToken != "" {
		t.Fatalf("stored explicit-empty media_user_token = %+v, want non-nil empty pointer", loaded.Overrides)
	}
}
