package db

import (
	"context"
	"testing"
	"time"

	"amdl/internal/domain"
)

func TestUserOverridesRoundTrip(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	embed := false
	priority := []string{"aac"}
	overrides := &domain.DownloadOverrides{EmbedLyrics: &embed, QualityPriority: &priority}

	created, err := store.CreateUser(ctx, domain.User{Username: "alice", Enabled: true, Overrides: overrides})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.GetUser(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Overrides == nil || got.Overrides.EmbedLyrics == nil || *got.Overrides.EmbedLyrics {
		t.Fatalf("overrides = %+v, want embed_lyrics=false", got.Overrides)
	}
	if got.Overrides.QualityPriority == nil || (*got.Overrides.QualityPriority)[0] != "aac" {
		t.Fatalf("quality priority = %+v", got.Overrides.QualityPriority)
	}
}

func TestUserWithoutOverridesLoadsNil(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	created, err := store.CreateUser(ctx, domain.User{Username: "bob", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.GetUser(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Overrides != nil {
		t.Fatalf("overrides = %+v, want nil", got.Overrides)
	}
}

func TestUpdateUserOverrides(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	created, err := store.CreateUser(ctx, domain.User{Username: "carol", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	retries := 5
	if err := store.UpdateUserOverrides(ctx, created.ID, &domain.DownloadOverrides{Retries: &retries}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetUser(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Overrides == nil || got.Overrides.Retries == nil || *got.Overrides.Retries != 5 {
		t.Fatalf("overrides = %+v, want retries 5", got.Overrides)
	}
	// Clearing stores '{}' which loads back as nil.
	if err := store.UpdateUserOverrides(ctx, created.ID, nil); err != nil {
		t.Fatal(err)
	}
	got, err = store.GetUser(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Overrides != nil {
		t.Fatalf("overrides = %+v, want nil after clear", got.Overrides)
	}
	if err := store.UpdateUserOverrides(ctx, "missing", nil); err == nil {
		t.Fatal("expected error for unknown user")
	}
}

// TestStoredOverridesTolerateUnknownKeys pins the lenient read-back contract:
// a row written by a different schema version (extra key) must still scan, or
// the owner would be locked out of auth and job recovery would abort startup.
func TestStoredOverridesTolerateUnknownKeys(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	created, err := store.CreateUser(ctx, domain.User{Username: "erin", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx,
		`UPDATE users SET overrides_json='{"retries":4,"future_field":true}' WHERE id=?`, created.ID); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetUser(ctx, created.ID)
	if err != nil {
		t.Fatalf("user row with unknown override key failed to scan: %v", err)
	}
	if got.Overrides == nil || got.Overrides.Retries == nil || *got.Overrides.Retries != 4 {
		t.Fatalf("overrides = %+v, want retries 4 with unknown key ignored", got.Overrides)
	}

	now := time.Now().UTC()
	job := domain.Job{
		ID: "job-future", Input: "https://music.apple.com/cn/song/z/3", Type: "song", Storefront: "cn",
		CanonicalKey: "song:cn:3", Status: domain.JobQueued, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx,
		`UPDATE jobs SET overrides_json='{"embed_lyrics":false,"future_field":1}' WHERE id=?`, job.ID); err != nil {
		t.Fatal(err)
	}
	gotJob, err := store.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("job row with unknown override key failed to scan: %v", err)
	}
	if gotJob.Overrides == nil || gotJob.Overrides.EmbedLyrics == nil || *gotJob.Overrides.EmbedLyrics {
		t.Fatalf("overrides = %+v, want embed_lyrics false with unknown key ignored", gotJob.Overrides)
	}
}

func TestJobOverridesRoundTrip(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	format := "{SongName}"
	job := domain.Job{
		ID: "job-ovr", Input: "https://music.apple.com/cn/song/x/1", Type: "song", Storefront: "cn",
		CanonicalKey: "song:cn:1", Status: domain.JobQueued, CreatedAt: now, UpdatedAt: now,
		Overrides: &domain.DownloadOverrides{SongFileFormat: &format},
	}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Overrides == nil || got.Overrides.SongFileFormat == nil || *got.Overrides.SongFileFormat != format {
		t.Fatalf("overrides = %+v, want song_file_format %q", got.Overrides, format)
	}

	plain := domain.Job{
		ID: "job-plain", Input: "https://music.apple.com/cn/song/y/2", Type: "song", Storefront: "cn",
		CanonicalKey: "song:cn:2", Status: domain.JobQueued, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateJob(ctx, plain); err != nil {
		t.Fatal(err)
	}
	gotPlain, err := store.GetJob(ctx, plain.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotPlain.Overrides != nil {
		t.Fatalf("overrides = %+v, want nil", gotPlain.Overrides)
	}
}
