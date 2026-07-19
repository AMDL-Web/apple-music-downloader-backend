package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func writeFullConfig(t *testing.T, path string, cfg Config) {
	t.Helper()
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureFileBootstrapsFromCombinedExample(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	if _, err := EnsureFile(path, filepath.Join(dir, "runtime.yaml")); err == nil {
		t.Fatal("expected an error when neither config nor example exists")
	}

	example := "# retained until the first API rewrite\n" +
		"server:\n  listen: \"127.0.0.1:19999\"\n" +
		"download:\n  cover_format: \"png\"\n"
	if err := os.WriteFile(filepath.Join(dir, configExampleFileName), []byte(example), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := EnsureFile(path, filepath.Join(dir, "runtime.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.CreatedConfig || result.MergedRuntime || result.RuntimeBackupPath != "" {
		t.Fatalf("bootstrap result = %+v", result)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != example {
		t.Fatalf("bootstrapped config is not the example's exact contents:\n%s", raw)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config permissions = %v, %v; want 0600", info.Mode().Perm(), err)
	}
	cfg, err := load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != "127.0.0.1:19999" || cfg.Download.CoverFormat != "png" {
		t.Fatalf("bootstrapped values lost: %+v", cfg)
	}

	// Once present, config.yaml is not replaced from the example.
	if err := os.WriteFile(path, []byte("server:\n  listen: \"127.0.0.1:20000\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err = EnsureFile(path, filepath.Join(dir, "runtime.yaml"))
	if err != nil || result.CreatedConfig || result.MergedRuntime {
		t.Fatalf("second bootstrap = (%+v, %v), want no-op", result, err)
	}
	if cfg, err := load(path, nil); err != nil || cfg.Server.Listen != "127.0.0.1:20000" {
		t.Fatalf("existing config overwritten: %+v, %v", cfg.Server, err)
	}
}

func TestEnsureFileMergesLegacyRuntimeWithRuntimeValuesWinning(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	runtimePath := filepath.Join(dir, "runtime.yaml")
	startup := "server:\n  listen: \"127.0.0.1:19999\"\n" +
		"wrapper:\n  address: \"wrapper.internal:8080\"\n" +
		"catalog:\n  album_track_url_mode: \"song\"\n  media_user_token: \"stale-token\"\n" +
		"download:\n  cover_format: \"jpg\"\n  max_running_jobs: 7\n"
	legacyRuntime := "logging:\n  level: \"debug\"\n" +
		"catalog:\n  album_track_url_mode: \"album\"\n  media_user_token: \"secret-token\"\n  media_user_token_priority: \"config\"\n" +
		"download:\n  cover_format: \"png\"\n  embed_lyrics: false\n"
	if err := os.WriteFile(configPath, []byte(startup), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimePath, []byte(legacyRuntime), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := EnsureFile(configPath, runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	if result.CreatedConfig || !result.MergedRuntime || result.RuntimeBackupPath == "" {
		t.Fatalf("migration result = %+v", result)
	}
	if _, err := os.Stat(runtimePath); !os.IsNotExist(err) {
		t.Fatalf("legacy runtime remains live: %v", err)
	}
	backup, err := os.ReadFile(result.RuntimeBackupPath)
	if err != nil || string(backup) != legacyRuntime {
		t.Fatalf("runtime backup = %q, %v; want exact original", backup, err)
	}
	cfg, err := load(configPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != "127.0.0.1:19999" || cfg.Wrapper.Address != "wrapper.internal:8080" || cfg.Download.MaxRunningJobs != 7 {
		t.Fatalf("startup values lost during merge: %+v", cfg)
	}
	if cfg.Logging.Level != "debug" || cfg.Catalog.AlbumTrackURLMode != "album" || cfg.Catalog.MediaUserToken != "secret-token" || cfg.Download.CoverFormat != "png" || cfg.Download.EmbedLyrics {
		t.Fatalf("legacy runtime values did not win: %+v", cfg)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"listen:", "address:", "album_track_url_mode:", "cover_format:", "max_running_jobs:"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("merged config missing %q:\n%s", want, raw)
		}
	}
	if strings.Contains(string(raw), "media_user_token_priority") {
		t.Fatalf("deprecated key survived merge:\n%s", raw)
	}
	if info, err := os.Stat(configPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("merged config permissions = %v, %v; want 0600", info.Mode().Perm(), err)
	}

	// Archiving the consumed runtime file makes all later starts no-ops.
	second, err := EnsureFile(configPath, runtimePath)
	if err != nil || second.CreatedConfig || second.MergedRuntime || second.RuntimeBackupPath != "" {
		t.Fatalf("second migration = (%+v, %v), want no-op", second, err)
	}
}

func TestEnsureFileBootstrapsThenMergesLegacyRuntime(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	runtimePath := filepath.Join(dir, "runtime.yaml")
	if err := os.WriteFile(filepath.Join(dir, configExampleFileName), []byte("server:\n  listen: \"127.0.0.1:19999\"\ndownload:\n  cover_format: \"jpg\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimePath, []byte("download:\n  cover_format: \"png\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := EnsureFile(configPath, runtimePath)
	if err != nil || !result.CreatedConfig || !result.MergedRuntime {
		t.Fatalf("bootstrap and merge = (%+v, %v)", result, err)
	}
	cfg, err := load(configPath, nil)
	if err != nil || cfg.Server.Listen != "127.0.0.1:19999" || cfg.Download.CoverFormat != "png" {
		t.Fatalf("merged config = %+v, %v", cfg, err)
	}
}

func TestEnsureFileRejectsInvalidLegacyRuntimeWithoutChangingFiles(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	runtimePath := filepath.Join(dir, "runtime.yaml")
	configRaw := []byte("server:\n  listen: \"127.0.0.1:19999\"\n")
	runtimeRaw := []byte("server:\n  listen: \"127.0.0.1:20000\"\n")
	if err := os.WriteFile(configPath, configRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimePath, runtimeRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureFile(configPath, runtimePath); err == nil || !strings.Contains(err.Error(), "startup-bound") {
		t.Fatalf("error = %v, want misplaced legacy key rejection", err)
	}
	if got, _ := os.ReadFile(configPath); !reflect.DeepEqual(got, configRaw) {
		t.Fatalf("failed migration changed config:\n%s", got)
	}
	if got, _ := os.ReadFile(runtimePath); !reflect.DeepEqual(got, runtimeRaw) {
		t.Fatalf("failed migration changed runtime:\n%s", got)
	}
}

func TestSaveRoundTripUsesOwnerOnlyPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	cfg.Server.Listen = "127.0.0.1:19999"
	cfg.Catalog.MediaUserToken = "secret-media-user-token"
	cfg.Download.CoverFormat = "png"
	cfg.Download.QualityPriority = []string{"aac"}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "# Managed by the amdl backend") || !strings.Contains(string(raw), "secret-media-user-token") {
		t.Fatalf("saved config missing header or token:\n%s", raw)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("saved config permissions = %v, %v; want 0600", info.Mode().Perm(), err)
	}
	loaded, err := load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Server.Listen != cfg.Server.Listen || loaded.Download.CoverFormat != "png" || !reflect.DeepEqual(loaded.Download.QualityPriority, []string{"aac"}) {
		t.Fatalf("round trip lost fields: %+v", loaded)
	}
	second := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(second, loaded); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(second)
	if string(raw) != string(again) {
		t.Fatalf("save is not stable across a load round trip:\n%s\n---\n%s", raw, again)
	}
}

func TestSaveUsesRandomAtomicTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	victim := filepath.Join(dir, "victim.txt")
	if err := os.WriteFile(victim, []byte("do not overwrite"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, path+".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, Default()); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(victim); err != nil || string(raw) != "do not overwrite" {
		t.Fatalf("fixed temp symlink target changed: %q, %v", raw, err)
	}
}

func TestStoreSetAndSavePersistsOnlyRuntimeFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	disk := Default()
	disk.Server.Listen = "127.0.0.1:19000"
	writeFullConfig(t, path, disk)

	running := disk
	running.Server.Listen = "127.0.0.1:18080"
	store := NewFileStore(running, path)
	updated := running
	updated.Server.Listen = "127.0.0.1:17000"
	updated.Download.EmbedLyrics = false
	if err := store.SetAndSave(updated); err != nil {
		t.Fatal(err)
	}
	if got := store.Get(); got.Server.Listen != "127.0.0.1:17000" || got.Download.EmbedLyrics {
		t.Fatalf("in-memory snapshot not replaced: %+v", got)
	}
	persisted, err := load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Server.Listen != "127.0.0.1:19000" {
		t.Fatalf("startup field was overwritten on save: %q", persisted.Server.Listen)
	}
	if persisted.Download.EmbedLyrics {
		t.Fatal("runtime field was not persisted")
	}

	mem := NewStore(running)
	if mem.Persistent() {
		t.Fatal("in-memory store must not report persistent")
	}
	if err := mem.SetAndSave(updated); err != nil || mem.Get().Download.EmbedLyrics {
		t.Fatalf("in-memory SetAndSave = %v, %+v", err, mem.Get())
	}
}

func TestStoreReloadAppliesOnlyRuntimeFieldsAndKeepsLastGoodSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	base := Default()
	base.Wrapper.Address = "running-wrapper:8080"
	base.Download.MaxRunningJobs = maxRunningJobsLimit
	writeFullConfig(t, path, base)
	store := NewFileStore(base, path)

	edited := base
	edited.Wrapper.Address = "next-restart-wrapper:8080"
	edited.Download.MaxRunningJobs = 1
	edited.Download.CoverFormat = "png"
	edited.Catalog.AlbumTrackURLMode = "album"
	edited.Logging.Level = "debug"
	writeFullConfig(t, path, edited)
	if err := store.Reload(); err != nil {
		t.Fatal(err)
	}
	got := store.Get()
	if got.Download.CoverFormat != "png" || got.Catalog.AlbumTrackURLMode != "album" || got.Logging.Level != "debug" {
		t.Fatalf("runtime edits not reloaded: %+v", got)
	}
	if got.Wrapper.Address != "running-wrapper:8080" || got.Download.MaxRunningJobs != maxRunningJobsLimit {
		t.Fatalf("startup edits became live: wrapper=%q max_running_jobs=%d", got.Wrapper.Address, got.Download.MaxRunningJobs)
	}

	if err := os.WriteFile(path, []byte("download: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Reload(); err == nil {
		t.Fatal("expected reload error for broken config")
	}
	if after := store.Get(); after.Download.CoverFormat != "png" || after.Wrapper.Address != "running-wrapper:8080" {
		t.Fatalf("failed reload changed snapshot: %+v", after)
	}
	if err := NewStore(Default()).Reload(); err != nil {
		t.Fatalf("in-memory reload = %v", err)
	}
}

func TestStoreReloadResetsDeletedRuntimeKeysButNotStartupFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	base := Default()
	base.Server.Listen = "127.0.0.1:19999"
	base.Download.CoverFormat = "png"
	writeFullConfig(t, path, base)
	store := NewFileStore(base, path)

	// A sparse file models a user deleting runtime keys. Load supplies their
	// defaults, while the startup field remains the process-start snapshot.
	if err := os.WriteFile(path, []byte("server:\n  listen: \"127.0.0.1:20000\"\nlogging:\n  level: \"warn\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Reload(); err != nil {
		t.Fatal(err)
	}
	got := store.Get()
	if got.Server.Listen != "127.0.0.1:19999" || got.Logging.Level != "warn" {
		t.Fatalf("reload field selection wrong: %+v", got)
	}
	if got.Download.CoverFormat != Default().Download.CoverFormat {
		t.Fatalf("deleted runtime key = %q, want default", got.Download.CoverFormat)
	}
}

func TestStoreUpdateAndSavePreservesPendingStartupEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	base := Default()
	writeFullConfig(t, path, base)
	store := NewFileStore(base, path)

	// The owner edits a startup field on disk but has not restarted yet.
	diskEdit := base
	diskEdit.Server.Listen = "127.0.0.1:19999"
	diskEdit.Download.CoverFormat = "png"
	writeFullConfig(t, path, diskEdit)
	updated, err := store.UpdateAndSave(func(current Config) (Config, error) {
		current.Download.EmbedCover = false
		return current, nil
	})
	if err != nil || updated.Download.EmbedCover {
		t.Fatalf("update = (%+v, %v)", updated.Download, err)
	}
	persisted, err := load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Server.Listen != "127.0.0.1:19999" {
		t.Fatalf("pending startup edit lost: %q", persisted.Server.Listen)
	}
	// PUT-before-GET semantics overwrite an unseen manual runtime edit with
	// the running snapshot while retaining the pending startup edit.
	if persisted.Download.CoverFormat != base.Download.CoverFormat || persisted.Download.EmbedCover {
		t.Fatalf("persisted runtime fields = %+v", persisted.Download)
	}
	if store.Get().Server.Listen != base.Server.Listen {
		t.Fatalf("pending startup edit became live: %q", store.Get().Server.Listen)
	}
}

func TestStoreSaveDoesNotPersistStartupEnvironmentOverride(t *testing.T) {
	t.Setenv("AMDL_SERVER_LISTEN", "127.0.0.1:17777")
	path := filepath.Join(t.TempDir(), "config.yaml")
	disk := Default()
	disk.Server.Listen = "127.0.0.1:18888"
	writeFullConfig(t, path, disk)
	effective, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	store := NewFileStore(effective, path)
	if _, err := store.UpdateAndSave(func(current Config) (Config, error) {
		current.Download.EmbedCover = false
		return current, nil
	}); err != nil {
		t.Fatal(err)
	}
	persisted, err := load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Server.Listen != "127.0.0.1:18888" {
		t.Fatalf("startup environment override was persisted: %q", persisted.Server.Listen)
	}
}

func TestStoreSaveMayPersistEffectiveRuntimeEnvironmentValue(t *testing.T) {
	// This preserves the existing contract: an unrelated PUT writes the
	// effective runtime snapshot, including unchanged runtime env overrides.
	t.Setenv("AMDL_DOWNLOAD_COVER_FORMAT", "png")
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeFullConfig(t, path, Default())
	effective, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	store := NewFileStore(effective, path)
	if _, err := store.UpdateAndSave(func(current Config) (Config, error) {
		current.Download.EmbedCover = false
		return current, nil
	}); err != nil {
		t.Fatal(err)
	}
	persisted, err := load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Download.CoverFormat != "png" || persisted.Download.EmbedCover {
		t.Fatalf("persisted runtime snapshot = %+v", persisted.Download)
	}
}

func TestStoreConcurrentUpdatesMergeSerially(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeFullConfig(t, path, Default())
	store := NewFileStore(Default(), path)
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := store.UpdateAndSave(func(current Config) (Config, error) {
			close(firstEntered)
			<-releaseFirst
			current.Download.CoverFormat = "png"
			return current, nil
		})
		errs <- err
	}()
	<-firstEntered
	go func() {
		defer wg.Done()
		_, err := store.UpdateAndSave(func(current Config) (Config, error) {
			current.Download.EmbedCover = false
			return current, nil
		})
		errs <- err
	}()
	close(releaseFirst)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got := store.Get()
	if got.Download.CoverFormat != "png" || got.Download.EmbedCover {
		t.Fatalf("concurrent update lost in memory: %+v", got.Download)
	}
	persisted, err := load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Download.CoverFormat != "png" || persisted.Download.EmbedCover {
		t.Fatalf("concurrent update lost on disk: %+v", persisted.Download)
	}
}

func TestStoreUpdateErrorLeavesSnapshotAndFileUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeFullConfig(t, path, Default())
	before, _ := os.ReadFile(path)
	store := NewFileStore(Default(), path)
	wantErr := os.ErrInvalid
	if _, err := store.UpdateAndSave(func(current Config) (Config, error) {
		current.Download.CoverFormat = "png"
		return current, wantErr
	}); err != wantErr {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	after, _ := os.ReadFile(path)
	if !reflect.DeepEqual(before, after) || store.Get().Download.CoverFormat != Default().Download.CoverFormat {
		t.Fatal("failed update changed snapshot or file")
	}
}
