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

func TestHooksOverrideNilAndEmptySurviveStorageRoundTrip(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	empty := []string{}
	tests := []struct {
		name  string
		hooks *[]string
	}{
		{name: "nil"},
		{name: "empty", hooks: &empty},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := domain.Job{
				ID: "job-hooks-" + tt.name, Input: "song|us|" + tt.name, Type: "song", CanonicalKey: "song:us:hooks-" + tt.name,
				Overrides: &config.DownloadOverrides{Hooks: tt.hooks},
				Status:    domain.JobQueued, CreatedAt: time.Now().UTC().Add(time.Duration(i) * time.Second), UpdatedAt: time.Now().UTC(),
			}
			if err := store.CreateJob(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			loaded, err := store.GetJob(context.Background(), job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Overrides == nil {
				t.Fatal("stored overrides decoded as nil")
			}
			if tt.hooks == nil {
				if loaded.Overrides.Hooks != nil {
					t.Fatalf("nil hooks decoded as %+v", loaded.Overrides.Hooks)
				}
			} else if loaded.Overrides.Hooks == nil || len(*loaded.Overrides.Hooks) != 0 {
				t.Fatalf("explicit-empty hooks decoded as %+v", loaded.Overrides.Hooks)
			}
		})
	}
}

func TestRemovedParallelTracksOverrideDoesNotBlockHistoricalJobLoad(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()

	now := time.Now().UTC()
	job := domain.Job{
		ID: "job-old-parallel", Input: "song|us|1", Type: "song", CanonicalKey: "song:us:old-parallel",
		Status: domain.JobQueued, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE jobs SET overrides=? WHERE id=?`, `{"max_parallel_tracks":64,"embed_lyrics":false}`, job.ID); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("load historical job: %v", err)
	}
	if loaded.Overrides == nil || loaded.Overrides.EmbedLyrics == nil || *loaded.Overrides.EmbedLyrics {
		t.Fatalf("historical supported overrides not retained: %+v", loaded.Overrides)
	}
}
