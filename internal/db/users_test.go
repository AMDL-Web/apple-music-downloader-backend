package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"amdl/internal/domain"
	_ "modernc.org/sqlite"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestUserCRUDRoundTrip(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	created, err := store.CreateUser(ctx, domain.User{
		Username: "lyjw", Role: domain.RoleAdmin, AvatarURL: "https://a/b.png", Enabled: true,
		Aliases: []string{"liang"}, Emails: []string{"lyjw@example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" {
		t.Fatal("id not generated")
	}

	got, err := store.GetUser(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != "lyjw" || got.Role != domain.RoleAdmin || !got.Enabled || got.AvatarURL != "https://a/b.png" {
		t.Fatalf("user = %+v", got)
	}
	if len(got.Aliases) != 1 || got.Aliases[0] != "liang" || len(got.Emails) != 1 {
		t.Fatalf("identities = %+v / %+v", got.Aliases, got.Emails)
	}

	got.Role = domain.RoleUser
	got.Enabled = false
	got.Aliases = []string{"ly", "jw"}
	got.Emails = []string{}
	if err := store.UpdateUser(ctx, got); err != nil {
		t.Fatal(err)
	}
	updated, err := store.GetUser(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Role != domain.RoleUser || updated.Enabled || len(updated.Aliases) != 2 || len(updated.Emails) != 0 {
		t.Fatalf("updated = %+v", updated)
	}

	users, err := store.ListUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].ID != created.ID {
		t.Fatalf("users = %+v", users)
	}
}

func TestCreateUserRejectsDuplicates(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if _, err := store.CreateUser(ctx, domain.User{Username: "lyjw", Enabled: true, Emails: []string{"a@b.c"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateUser(ctx, domain.User{Username: "LYJW", Enabled: true}); !IsConflict(err) {
		t.Fatalf("case-insensitive duplicate username err = %v, want conflict", err)
	}
	if _, err := store.CreateUser(ctx, domain.User{Username: "other", Enabled: true, Emails: []string{"A@B.C"}}); !IsConflict(err) {
		t.Fatalf("duplicate email err = %v, want conflict", err)
	}
}

func TestResolveIdentityOrder(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	alpha, err := store.CreateUser(ctx, domain.User{Username: "alpha", Enabled: true, Aliases: []string{"al"}, Emails: []string{"alpha@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	beta, err := store.CreateUser(ctx, domain.User{Username: "beta", Enabled: true, Emails: []string{"beta@example.com"}})
	if err != nil {
		t.Fatal(err)
	}

	// X-User match wins over X-Email.
	got, err := store.ResolveIdentity(ctx, "AL", "beta@example.com")
	if err != nil || got.ID != alpha.ID {
		t.Fatalf("got = %+v, err = %v, want alpha", got, err)
	}
	// Unmatched X-User falls back to X-Email.
	got, err = store.ResolveIdentity(ctx, "nobody", "BETA@example.com")
	if err != nil || got.ID != beta.ID {
		t.Fatalf("got = %+v, err = %v, want beta", got, err)
	}
	if _, err := store.ResolveIdentity(ctx, "nobody", "nobody@example.com"); err != sql.ErrNoRows {
		t.Fatalf("err = %v, want sql.ErrNoRows", err)
	}
}

func TestEnsureBootstrapAdmin(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	admin, err := store.EnsureBootstrapAdmin(ctx, "lyjw", "lyjw@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if admin.Role != domain.RoleAdmin || !admin.Enabled || admin.Username != "lyjw" {
		t.Fatalf("admin = %+v", admin)
	}

	// Second run is idempotent and does not create another user.
	again, err := store.EnsureBootstrapAdmin(ctx, "lyjw", "")
	if err != nil || again.ID != admin.ID {
		t.Fatalf("again = %+v, err = %v", again, err)
	}
	if count, _ := store.CountUsers(ctx); count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}

	// Non-empty table with a different bootstrap name is ignored.
	other, err := store.EnsureBootstrapAdmin(ctx, "someoneelse", "")
	if err != nil || other.ID != "" {
		t.Fatalf("other = %+v, err = %v, want zero user", other, err)
	}

	if _, err := store.ResolveIdentity(ctx, "", "lyjw@example.com"); err != nil {
		t.Fatalf("bootstrap email identity missing: %v", err)
	}
}

func TestJobUserAttributionAndFilter(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	user, err := store.CreateUser(ctx, domain.User{Username: "lyjw", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	mine := domain.Job{ID: "job-mine", UserID: user.ID, Input: "https://music.apple.com/cn/song/x/1", Type: "song", Status: domain.JobQueued, CreatedAt: now, UpdatedAt: now}
	orphan := domain.Job{ID: "job-orphan", Input: "https://music.apple.com/cn/song/y/2", Type: "song", Status: domain.JobQueued, CreatedAt: now.Add(time.Second), UpdatedAt: now}
	for _, job := range []domain.Job{mine, orphan} {
		if err := store.CreateJob(ctx, job); err != nil {
			t.Fatal(err)
		}
	}

	got, err := store.GetJob(ctx, "job-mine")
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID != user.ID || got.Username != "lyjw" {
		t.Fatalf("job attribution = %q/%q", got.UserID, got.Username)
	}

	all, err := store.ListJobs(ctx, 0, "")
	if err != nil || len(all) != 2 {
		t.Fatalf("all = %d jobs, err = %v", len(all), err)
	}
	filtered, err := store.ListJobs(ctx, 0, user.ID)
	if err != nil || len(filtered) != 1 || filtered[0].ID != "job-mine" {
		t.Fatalf("filtered = %+v, err = %v", filtered, err)
	}

	assigned, err := store.AssignJobsWithoutUser(ctx, user.ID)
	if err != nil || assigned != 1 {
		t.Fatalf("assigned = %d, err = %v", assigned, err)
	}
	filtered, err = store.ListJobs(ctx, 0, user.ID)
	if err != nil || len(filtered) != 2 {
		t.Fatalf("after assign filtered = %d jobs, err = %v", len(filtered), err)
	}
}

// TestMigrationAddsUserIDColumn opens a database created with the pre-multi-user
// jobs schema and verifies Open adds jobs.user_id without losing rows.
func TestMigrationAddsUserIDColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`CREATE TABLE jobs (
			id TEXT PRIMARY KEY,
			input TEXT NOT NULL,
			type TEXT NOT NULL,
			storefront TEXT,
			force INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL,
			total_items INTEGER NOT NULL DEFAULT 0,
			done_items INTEGER NOT NULL DEFAULT 0,
			failed_items INTEGER NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`INSERT INTO jobs(id,input,type,storefront,status,created_at,updated_at)
			VALUES('job-legacy','https://music.apple.com/cn/song/x/1','song','cn','completed','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');`,
	}
	for _, stmt := range stmts {
		if _, err := legacy.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job, err := store.GetJob(context.Background(), "job-legacy")
	if err != nil {
		t.Fatal(err)
	}
	if job.UserID != "" || job.Username != "" || job.Status != domain.JobCompleted {
		t.Fatalf("legacy job = %+v", job)
	}
}
