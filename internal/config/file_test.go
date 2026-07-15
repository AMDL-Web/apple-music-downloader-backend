package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBootstrapFromExample(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// No config and no example: nothing to start from.
	if _, err := BootstrapFromExample(path); err == nil {
		t.Fatal("expected an error when neither config nor example exists")
	}

	example := "server:\n  listen: \"127.0.0.1:19999\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.example.yaml"), []byte(example), 0o644); err != nil {
		t.Fatal(err)
	}
	created, err := BootstrapFromExample(path)
	if err != nil || !created {
		t.Fatalf("bootstrap = (%v, %v), want created", created, err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load bootstrapped config: %v", err)
	}
	if cfg.Server.Listen != "127.0.0.1:19999" {
		t.Fatalf("bootstrapped listen = %q", cfg.Server.Listen)
	}
	// The live file is written in the machine-managed format from the start;
	// the example's comments are not copied over.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "# Managed by the amdl backend") {
		t.Fatalf("bootstrapped file missing managed-file header: %q", string(raw)[:60])
	}

	// An existing config is left untouched.
	if err := os.WriteFile(path, []byte("server:\n  listen: \"127.0.0.1:20000\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	created, err = BootstrapFromExample(path)
	if err != nil || created {
		t.Fatalf("second bootstrap = (%v, %v), want untouched", created, err)
	}
	if cfg, err := Load(path); err != nil || cfg.Server.Listen != "127.0.0.1:20000" {
		t.Fatalf("existing config overwritten: %+v, %v", cfg.Server, err)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := Default()
	cfg.Download.CoverFormat = "png"
	cfg.Download.QualityPriority = []string{"aac"}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "# Managed by the amdl backend") {
		t.Fatalf("saved file missing managed-file header: %q", string(raw)[:80])
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload saved config: %v", err)
	}
	if loaded.Download.CoverFormat != "png" || !reflect.DeepEqual(loaded.Download.QualityPriority, []string{"aac"}) {
		t.Fatalf("changed fields lost in round trip: %+v", loaded.Download)
	}
	if loaded.Server.Listen != cfg.Server.Listen || loaded.Download.SongPathFormat != cfg.Download.SongPathFormat {
		t.Fatalf("unchanged fields lost in round trip: %+v", loaded)
	}
	// Saving the reloaded config must be byte-stable (a nil slice may load
	// back as an empty one, but the serialized form must not oscillate).
	second := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(second, loaded); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(path)
	again, _ := os.ReadFile(second)
	if string(first) != string(again) {
		t.Fatalf("save is not stable across a load round trip:\n%s\n---\n%s", first, again)
	}
}

func TestSaveUsesOwnerOnlyPermissionsForPersistedToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	// Older releases wrote 0644. Saving over such a file must both preserve
	// compatibility and tighten its replacement to 0600.
	if err := os.WriteFile(path, []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	cfg.Catalog.MediaUserToken = "secret-media-user-token"
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("saved config permissions = %#o, want 0600", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "secret-media-user-token") {
		t.Fatal("media_user_token was not persisted")
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Catalog.MediaUserToken != cfg.Catalog.MediaUserToken {
		t.Fatalf("reloaded media_user_token = %q", loaded.Catalog.MediaUserToken)
	}
}

func TestStoreSetAndSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	store := NewFileStore(Default(), path)
	if !store.Persistent() {
		t.Fatal("file store must report persistent")
	}
	updated := Default()
	updated.Download.EmbedLyrics = false
	if err := store.SetAndSave(updated); err != nil {
		t.Fatal(err)
	}
	if store.Get().Download.EmbedLyrics {
		t.Fatal("snapshot not updated")
	}
	if loaded, err := Load(path); err != nil || loaded.Download.EmbedLyrics {
		t.Fatalf("saved file not updated: %+v, %v", loaded.Download, err)
	}

	// In-memory stores just swap the snapshot.
	mem := NewStore(Default())
	if mem.Persistent() {
		t.Fatal("in-memory store must not report persistent")
	}
	if err := mem.SetAndSave(updated); err != nil {
		t.Fatal(err)
	}
	if mem.Get().Download.EmbedLyrics {
		t.Fatal("in-memory snapshot not updated")
	}
}

func TestStoreReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	store := NewFileStore(Default(), path)
	edited := Default()
	edited.Download.CoverFormat = "png"
	if err := Save(path, edited); err != nil {
		t.Fatal(err)
	}
	if err := store.Reload(); err != nil {
		t.Fatal(err)
	}
	if store.Get().Download.CoverFormat != "png" {
		t.Fatalf("reload did not pick up file edit: %+v", store.Get().Download)
	}

	// A broken file leaves the snapshot untouched.
	if err := os.WriteFile(path, []byte("download: ["), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Reload(); err == nil {
		t.Fatal("expected reload error for broken file")
	}
	if store.Get().Download.CoverFormat != "png" {
		t.Fatalf("failed reload changed the snapshot: %+v", store.Get().Download)
	}

	// In-memory stores are a no-op.
	if err := NewStore(Default()).Reload(); err != nil {
		t.Fatalf("in-memory reload = %v", err)
	}
}

func TestReloadPreservesLockedFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	store := NewFileStore(Default(), path)

	// A manual edit changes one mutable and several startup-bound fields.
	edited := Default()
	edited.Download.CoverFormat = "png"
	edited.Catalog.AlbumTrackURLMode = "album"
	edited.Tools.FFmpeg = "/opt/other/ffmpeg"
	edited.Wrapper.Address = "10.0.0.9:8080"
	edited.Logging.Level = "debug"
	edited.Logging.Format = "json"
	edited.Download.MaxRunningJobs = maxRunningJobsLimit
	if err := Save(path, edited); err != nil {
		t.Fatal(err)
	}
	if err := store.Reload(); err != nil {
		t.Fatal(err)
	}
	got := store.Get()
	if got.Download.CoverFormat != "png" || got.Catalog.AlbumTrackURLMode != "album" || got.Logging.Level != "debug" {
		t.Fatalf("mutable edits not reloaded: %+v", got.Download)
	}
	base := Default()
	if got.Tools.FFmpeg != base.Tools.FFmpeg || got.Wrapper.Address != base.Wrapper.Address || got.Logging.Format != base.Logging.Format || got.Download.MaxRunningJobs != base.Download.MaxRunningJobs {
		t.Fatalf("startup-bound file edits leaked into the live snapshot: tools=%+v wrapper=%+v logging=%+v max_running_jobs=%d", got.Tools, got.Wrapper, got.Logging, got.Download.MaxRunningJobs)
	}
}

func TestStoreUpdateAndSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	store := NewFileStore(Default(), path)

	updated, err := store.UpdateAndSave(func(current Config) (Config, error) {
		current.Download.CoverFormat = "png"
		return current, nil
	})
	if err != nil || updated.Download.CoverFormat != "png" {
		t.Fatalf("update = (%+v, %v)", updated.Download, err)
	}
	if store.Get().Download.CoverFormat != "png" {
		t.Fatal("snapshot not updated")
	}
	if loaded, err := Load(path); err != nil || loaded.Download.CoverFormat != "png" {
		t.Fatalf("file not updated: %v", err)
	}

	// A mutate error leaves snapshot and file untouched.
	wantErr := os.ErrInvalid
	if _, err := store.UpdateAndSave(func(current Config) (Config, error) {
		current.Download.CoverFormat = "jpeg"
		return current, wantErr
	}); err != wantErr {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if store.Get().Download.CoverFormat != "png" {
		t.Fatal("failed update changed the snapshot")
	}
}
