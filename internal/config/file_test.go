package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// splitPaths returns the standard startup/runtime file pair inside dir,
// creating an empty startup file so loadPair can run against runtime-file
// tests that have no startup content of their own.
func splitPaths(t *testing.T, dir string) (string, string) {
	t.Helper()
	startup := filepath.Join(dir, "config.yaml")
	if _, err := os.Stat(startup); os.IsNotExist(err) {
		if err := os.WriteFile(startup, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return startup, filepath.Join(dir, "runtime.yaml")
}

func TestEnsureFilesBootstrapsFromExamples(t *testing.T) {
	dir := t.TempDir()
	startup := filepath.Join(dir, "config.yaml")
	runtime := filepath.Join(dir, "runtime.yaml")

	// No config and no example: nothing to start from.
	if _, err := EnsureFiles(startup, runtime); err == nil {
		t.Fatal("expected an error when neither config nor example exists")
	}

	startupExample := "# my startup comment\nserver:\n  listen: \"127.0.0.1:19999\"\n"
	runtimeExample := "download:\n  cover_format: \"png\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.example.yaml"), []byte(startupExample), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runtime.example.yaml"), []byte(runtimeExample), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := EnsureFiles(startup, runtime)
	if err != nil || !result.CreatedStartup || !result.CreatedRuntime || result.MigratedLegacy {
		t.Fatalf("bootstrap = (%+v, %v), want both files created", result, err)
	}
	cfg, err := loadPair(startup, runtime, nil)
	if err != nil {
		t.Fatalf("load bootstrapped configs: %v", err)
	}
	if cfg.Server.Listen != "127.0.0.1:19999" || cfg.Download.CoverFormat != "png" {
		t.Fatalf("bootstrapped values lost: listen=%q cover_format=%q", cfg.Server.Listen, cfg.Download.CoverFormat)
	}
	// A pre-split (startup-only) example is copied verbatim so its comments
	// survive into the now owner-edited live startup file.
	raw, err := os.ReadFile(startup)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != startupExample {
		t.Fatalf("startup file is not a verbatim example copy:\n%s", raw)
	}
	// The runtime file is machine-managed from the start.
	raw, err = os.ReadFile(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "# Managed by the amdl backend") {
		t.Fatalf("runtime file missing managed-file header: %q", string(raw)[:60])
	}

	// Existing files are left untouched.
	if err := os.WriteFile(startup, []byte("server:\n  listen: \"127.0.0.1:20000\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err = EnsureFiles(startup, runtime)
	if err != nil || result.CreatedStartup || result.CreatedRuntime || result.MigratedLegacy {
		t.Fatalf("second bootstrap = (%+v, %v), want untouched", result, err)
	}
	if cfg, err := loadPair(startup, runtime, nil); err != nil || cfg.Server.Listen != "127.0.0.1:20000" {
		t.Fatalf("existing config overwritten: %+v, %v", cfg.Server, err)
	}
}

func TestEnsureFilesBootstrapsFromLegacyCombinedExample(t *testing.T) {
	// A deployment upgraded in place still has the old combined example and
	// no runtime.example.yaml: both live files are extracted from it.
	dir := t.TempDir()
	startup := filepath.Join(dir, "config.yaml")
	runtime := filepath.Join(dir, "runtime.yaml")
	combined := "server:\n  listen: \"127.0.0.1:19999\"\ndownload:\n  cover_format: \"png\"\n  max_running_jobs: 7\n"
	if err := os.WriteFile(filepath.Join(dir, "config.example.yaml"), []byte(combined), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := EnsureFiles(startup, runtime)
	if err != nil || !result.CreatedStartup || !result.CreatedRuntime {
		t.Fatalf("bootstrap = (%+v, %v), want both files created", result, err)
	}
	cfg, err := loadPair(startup, runtime, nil)
	if err != nil {
		t.Fatalf("load bootstrapped configs: %v", err)
	}
	if cfg.Server.Listen != "127.0.0.1:19999" || cfg.Download.CoverFormat != "png" || cfg.Download.MaxRunningJobs != 7 {
		t.Fatalf("example values lost: %+v", cfg.Download)
	}
}

func TestEnsureFilesMigratesLegacyCombinedConfig(t *testing.T) {
	dir := t.TempDir()
	startup := filepath.Join(dir, "config.yaml")
	runtime := filepath.Join(dir, "runtime.yaml")
	legacy := "server:\n  listen: \"127.0.0.1:19999\"\n" +
		"catalog:\n  media_user_token: \"secret-token\"\n  media_user_token_priority: \"config\"\n" +
		"download:\n  cover_format: \"png\"\n  max_running_jobs: 7\n"
	if err := os.WriteFile(startup, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := EnsureFiles(startup, runtime)
	if err != nil || !result.MigratedLegacy || result.CreatedStartup || result.CreatedRuntime {
		t.Fatalf("migration = (%+v, %v), want MigratedLegacy", result, err)
	}
	if result.LegacyBackupPath == "" {
		t.Fatal("migration did not report a backup path")
	}
	backup, err := os.ReadFile(result.LegacyBackupPath)
	if err != nil || string(backup) != legacy {
		t.Fatalf("backup does not preserve the combined file: %v", err)
	}

	cfg, err := loadPair(startup, runtime, nil)
	if err != nil {
		t.Fatalf("load migrated configs: %v", err)
	}
	if cfg.Server.Listen != "127.0.0.1:19999" || cfg.Catalog.MediaUserToken != "secret-token" ||
		cfg.Download.CoverFormat != "png" || cfg.Download.MaxRunningJobs != 7 {
		t.Fatalf("migrated values lost: %+v", cfg)
	}
	// The runtime keys must have left the startup file, the startup keys must
	// not be in the runtime file, and the deprecated priority key is dropped.
	startupRaw, _ := os.ReadFile(startup)
	runtimeRaw, _ := os.ReadFile(runtime)
	if strings.Contains(string(startupRaw), "cover_format") || strings.Contains(string(startupRaw), "media_user_token") {
		t.Fatalf("runtime keys left in the startup file:\n%s", startupRaw)
	}
	if !strings.Contains(string(startupRaw), "max_running_jobs: 7") {
		t.Fatalf("startup keys lost from the startup file:\n%s", startupRaw)
	}
	if strings.Contains(string(runtimeRaw), "listen") || strings.Contains(string(runtimeRaw), "max_running_jobs") {
		t.Fatalf("startup keys leaked into the runtime file:\n%s", runtimeRaw)
	}
	if strings.Contains(string(runtimeRaw), "media_user_token_priority") {
		t.Fatalf("deprecated key survived migration:\n%s", runtimeRaw)
	}
	if info, err := os.Stat(runtime); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("runtime file permissions = %v, %v; want 0600", info.Mode().Perm(), err)
	}

	// Migration happens once: a second EnsureFiles run is a no-op.
	result, err = EnsureFiles(startup, runtime)
	if err != nil || result.MigratedLegacy || result.CreatedStartup || result.CreatedRuntime {
		t.Fatalf("second run = (%+v, %v), want no-op", result, err)
	}
}

func TestEnsureFilesCreatesRuntimeForStartupOnlyConfig(t *testing.T) {
	// A hand-written minimal config with no runtime keys is not a migration:
	// the runtime file is created from the example (falling back to defaults).
	dir := t.TempDir()
	startup := filepath.Join(dir, "config.yaml")
	runtime := filepath.Join(dir, "runtime.yaml")
	if err := os.WriteFile(startup, []byte("server:\n  listen: \"127.0.0.1:19999\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := EnsureFiles(startup, runtime)
	if err != nil || !result.CreatedRuntime || result.MigratedLegacy || result.CreatedStartup {
		t.Fatalf("result = (%+v, %v), want CreatedRuntime only", result, err)
	}
	cfg, err := loadPair(startup, runtime, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Download.CoverFormat != Default().Download.CoverFormat {
		t.Fatalf("runtime defaults lost: %+v", cfg.Download)
	}
}

func TestLoadPairRejectsMisplacedKeys(t *testing.T) {
	dir := t.TempDir()
	startup, runtime := splitPaths(t, dir)
	if err := os.WriteFile(runtime, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	// A runtime key in the startup file is a load error, not a silently
	// shadowed value.
	if err := os.WriteFile(startup, []byte("download:\n  cover_format: \"png\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPair(startup, runtime, nil); err == nil || !strings.Contains(err.Error(), "runtime-managed") {
		t.Fatalf("err = %v, want runtime-managed key rejection", err)
	}

	// And vice versa.
	if err := os.WriteFile(startup, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtime, []byte("server:\n  listen: \":1\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPair(startup, runtime, nil); err == nil || !strings.Contains(err.Error(), "startup-bound") {
		t.Fatalf("err = %v, want startup-bound key rejection", err)
	}

	// Unknown keys and sections are named.
	if err := os.WriteFile(runtime, []byte("download:\n  codec: \"alac\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPair(startup, runtime, nil); err == nil || !strings.Contains(err.Error(), "download.codec") {
		t.Fatalf("err = %v, want unknown key error", err)
	}
	if err := os.WriteFile(runtime, []byte("dwnload:\n  cover_format: \"png\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPair(startup, runtime, nil); err == nil || !strings.Contains(err.Error(), "dwnload") {
		t.Fatalf("err = %v, want unknown section error", err)
	}
}

func TestSaveRuntimeRoundTrip(t *testing.T) {
	dir := t.TempDir()
	startup, runtime := splitPaths(t, dir)
	cfg := Default()
	cfg.Download.CoverFormat = "png"
	cfg.Download.QualityPriority = []string{"aac"}
	if err := SaveRuntime(runtime, cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "# Managed by the amdl backend") {
		t.Fatalf("saved file missing managed-file header: %q", string(raw)[:80])
	}
	if strings.Contains(string(raw), "listen") || strings.Contains(string(raw), "max_running_jobs") {
		t.Fatalf("startup-bound keys written to the runtime file:\n%s", raw)
	}
	loaded, err := loadPair(startup, runtime, nil)
	if err != nil {
		t.Fatalf("reload saved config: %v", err)
	}
	if loaded.Download.CoverFormat != "png" || !reflect.DeepEqual(loaded.Download.QualityPriority, []string{"aac"}) {
		t.Fatalf("changed fields lost in round trip: %+v", loaded.Download)
	}
	if loaded.Download.SongPathFormat != cfg.Download.SongPathFormat {
		t.Fatalf("unchanged fields lost in round trip: %+v", loaded)
	}
	// Saving the reloaded config must be byte-stable (a nil slice may load
	// back as an empty one, but the serialized form must not oscillate).
	second := filepath.Join(t.TempDir(), "runtime.yaml")
	if err := SaveRuntime(second, loaded); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(runtime)
	again, _ := os.ReadFile(second)
	if string(first) != string(again) {
		t.Fatalf("save is not stable across a load round trip:\n%s\n---\n%s", first, again)
	}
}

func TestSaveRuntimeUsesOwnerOnlyPermissionsForPersistedToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.yaml")
	// Older releases wrote 0644. Saving over such a file must both preserve
	// compatibility and tighten its replacement to 0600.
	if err := os.WriteFile(path, []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	cfg.Catalog.MediaUserToken = "secret-media-user-token"
	if err := SaveRuntime(path, cfg); err != nil {
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
}

func TestSaveRuntimeDoesNotFollowLegacyFixedTempSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.yaml")
	victim := filepath.Join(dir, "victim.txt")
	if err := os.WriteFile(victim, []byte("do not overwrite"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Older Save implementations opened this predictable name with O_TRUNC.
	if err := os.Symlink(victim, path+".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := SaveRuntime(path, Default()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "do not overwrite" {
		t.Fatalf("fixed temp symlink target was overwritten: %q", raw)
	}
}

func TestStoreSetAndSave(t *testing.T) {
	dir := t.TempDir()
	startup, runtime := splitPaths(t, dir)
	store := NewFileStore(Default(), runtime)
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
	if loaded, err := loadPair(startup, runtime, nil); err != nil || loaded.Download.EmbedLyrics {
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
	path := filepath.Join(t.TempDir(), "runtime.yaml")
	store := NewFileStore(Default(), path)
	edited := Default()
	edited.Download.CoverFormat = "png"
	if err := SaveRuntime(path, edited); err != nil {
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

func TestStoreReloadKeepsStartupFieldsAndResetsDeletedKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.yaml")
	base := Default()
	base.Wrapper.Address = "10.0.0.9:8080"
	base.Download.MaxRunningJobs = maxRunningJobsLimit
	store := NewFileStore(base, path)

	edited := Default()
	edited.Download.CoverFormat = "png"
	edited.Catalog.AlbumTrackURLMode = "album"
	edited.Logging.Level = "debug"
	if err := SaveRuntime(path, edited); err != nil {
		t.Fatal(err)
	}
	if err := store.Reload(); err != nil {
		t.Fatal(err)
	}
	got := store.Get()
	if got.Download.CoverFormat != "png" || got.Catalog.AlbumTrackURLMode != "album" || got.Logging.Level != "debug" {
		t.Fatalf("mutable edits not reloaded: %+v", got.Download)
	}
	// Startup-bound fields keep their in-memory values: the runtime file
	// cannot carry them at all.
	if got.Wrapper.Address != "10.0.0.9:8080" || got.Download.MaxRunningJobs != maxRunningJobsLimit {
		t.Fatalf("startup fields lost on reload: wrapper=%+v max_running_jobs=%d", got.Wrapper, got.Download.MaxRunningJobs)
	}

	// A startup-bound key hand-edited into the runtime file fails the reload
	// and leaves the snapshot untouched.
	if err := os.WriteFile(path, []byte("tools:\n  ffmpeg: \"/opt/other/ffmpeg\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Reload(); err == nil || !strings.Contains(err.Error(), "startup-bound") {
		t.Fatalf("err = %v, want startup-bound rejection", err)
	}
	if store.Get().Tools.FFmpeg != Default().Tools.FFmpeg {
		t.Fatal("failed reload changed the snapshot")
	}

	// A runtime key deleted from the file resets to its built-in default,
	// exactly as a fresh load would see it.
	if err := os.WriteFile(path, []byte("logging:\n  level: \"warn\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Reload(); err != nil {
		t.Fatal(err)
	}
	got = store.Get()
	if got.Logging.Level != "warn" {
		t.Fatalf("logging.level = %q, want warn", got.Logging.Level)
	}
	if got.Download.CoverFormat != Default().Download.CoverFormat {
		t.Fatalf("deleted runtime key did not reset to default: %q", got.Download.CoverFormat)
	}
}

func TestStoreUpdateAndSave(t *testing.T) {
	dir := t.TempDir()
	startup, runtime := splitPaths(t, dir)
	store := NewFileStore(Default(), runtime)

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
	if loaded, err := loadPair(startup, runtime, nil); err != nil || loaded.Download.CoverFormat != "png" {
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
