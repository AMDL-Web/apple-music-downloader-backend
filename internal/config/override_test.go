package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDownloadOverridesApplyNilKeepsBase(t *testing.T) {
	base := Default()
	var overrides *DownloadOverrides
	if got := overrides.Apply(base); !reflect.DeepEqual(got, base) {
		t.Fatalf("nil overrides changed config: %+v", got)
	}
}

func TestDownloadOverridesApplyMergesOnlySetFields(t *testing.T) {
	base := Default()
	embed := false
	format := "png"
	memoryMode := MemoryModeHigh
	quality := []string{"aac"}
	mediaUserToken := "job-token"
	base.Catalog.MediaUserToken = "global-token"
	overrides := &DownloadOverrides{
		MediaUserToken:  &mediaUserToken,
		QualityPriority: &quality,
		EmbedCover:      &embed,
		CoverFormat:     &format,
		MemoryMode:      &memoryMode,
	}
	got := overrides.Apply(base)

	if !reflect.DeepEqual(got.Download.QualityPriority, []string{"aac"}) {
		t.Fatalf("quality_priority = %v, want [aac]", got.Download.QualityPriority)
	}
	if got.Download.EmbedCover != false || got.Download.CoverFormat != "png" || got.Download.MemoryMode != MemoryModeHigh {
		t.Fatalf("overridden fields not applied: %+v", got.Download)
	}
	if got.Catalog.MediaUserToken != "job-token" {
		t.Fatalf("media_user_token = %q, want job-token", got.Catalog.MediaUserToken)
	}
	// Untouched fields keep the base values, including false-able booleans.
	if got.Download.EmbedLyrics != base.Download.EmbedLyrics || got.Download.SongPathFormat != base.Download.SongPathFormat {
		t.Fatalf("unset fields changed: %+v", got.Download)
	}
	// The base config must not be mutated in place.
	if base.Download.EmbedCover != true || base.Download.CoverFormat != "jpg" {
		t.Fatalf("Apply mutated the base config: %+v", base.Download)
	}
	if base.Catalog.MediaUserToken != "global-token" {
		t.Fatalf("Apply mutated the base catalog config: %+v", base.Catalog)
	}
}

func TestDownloadOverridesApplyForceOverwrite(t *testing.T) {
	forceOn := true
	forceOff := false

	base := Default()
	if got := (&DownloadOverrides{ForceOverwrite: &forceOn}).Apply(base); !got.Download.ForceOverwrite {
		t.Fatal("force_overwrite override true was not applied over the false default")
	}

	base.Download.ForceOverwrite = true
	if got := (&DownloadOverrides{ForceOverwrite: &forceOff}).Apply(base); got.Download.ForceOverwrite {
		t.Fatal("force_overwrite override false did not win over the global true")
	}
	if got := (&DownloadOverrides{}).Apply(base); !got.Download.ForceOverwrite {
		t.Fatal("unset force_overwrite override changed the global value")
	}
}

func TestDownloadOverridesMediaUserTokenThreeState(t *testing.T) {
	base := Default()
	base.Catalog.MediaUserToken = "global-token"
	if got := (&DownloadOverrides{}).Apply(base).Catalog.MediaUserToken; got != "global-token" {
		t.Fatalf("absent override token = %q, want global-token", got)
	}
	empty := ""
	if got := (&DownloadOverrides{MediaUserToken: &empty}).Apply(base).Catalog.MediaUserToken; got != "" {
		t.Fatalf("explicit empty override token = %q, want empty", got)
	}
}

func TestDownloadOverridesRequestJSONIncludesMediaUserToken(t *testing.T) {
	token := "secret-token"
	embed := false
	raw, err := json.Marshal(&DownloadOverrides{MediaUserToken: &token, EmbedCover: &embed})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"media_user_token":"secret-token"`) {
		t.Fatalf("request override JSON lost media-user-token: %s", raw)
	}
	if !strings.Contains(string(raw), `"embed_cover":false`) {
		t.Fatalf("public override JSON lost ordinary field: %s", raw)
	}
}

func TestDownloadOverridesIgnoresRemovedParallelTracksInPersistedJSON(t *testing.T) {
	var overrides DownloadOverrides
	if err := json.Unmarshal([]byte(`{"max_parallel_tracks":64,"embed_cover":false}`), &overrides); err != nil {
		t.Fatalf("decode historical override: %v", err)
	}
	if overrides.EmbedCover == nil || *overrides.EmbedCover {
		t.Fatalf("supported historical fields were not decoded: %+v", overrides)
	}
	if raw, err := json.Marshal(overrides); err != nil || strings.Contains(string(raw), "max_parallel_tracks") {
		t.Fatalf("removed override field survived normalization: raw=%s err=%v", raw, err)
	}
}

func TestDownloadOverridesApplyThenValidate(t *testing.T) {
	bad := "gif"
	overrides := &DownloadOverrides{CoverFormat: &bad}
	if err := overrides.Apply(Default()).Validate(); err == nil {
		t.Fatal("expected validation error for cover_format=gif")
	}
	good := "png"
	overrides = &DownloadOverrides{CoverFormat: &good}
	if err := overrides.Apply(Default()).Validate(); err != nil {
		t.Fatalf("valid overrides rejected: %v", err)
	}
	badMemoryMode := "auto"
	if err := (&DownloadOverrides{MemoryMode: &badMemoryMode}).Apply(Default()).Validate(); err == nil || !strings.Contains(err.Error(), "memory_mode") {
		t.Fatalf("invalid memory_mode override error = %v, want memory_mode validation error", err)
	}
}

func TestDownloadOverridesKeepFilesystemRootsContained(t *testing.T) {
	dir := t.TempDir()
	downloadsRoot := filepath.Join(dir, "downloads")
	tempRoot := filepath.Join(dir, "temp")
	outside := filepath.Join(dir, "outside")
	for _, path := range []string{downloadsRoot, tempRoot, outside} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	base := Default()
	base.Download.DownloadsDir = downloadsRoot
	base.Download.TempDir = tempRoot

	insideDownloads := filepath.Join(downloadsRoot, "new", "nested")
	insideTemp := filepath.Join(tempRoot, "job-123")
	if _, err := (&DownloadOverrides{DownloadsDir: &insideDownloads, TempDir: &insideTemp}).ApplyValidated(base); err != nil {
		t.Fatalf("nonexistent subdirectories rejected: %v", err)
	}

	escaped := filepath.Join(downloadsRoot, "..", "outside")
	if _, err := (&DownloadOverrides{DownloadsDir: &escaped}).ApplyValidated(base); err == nil || !strings.Contains(err.Error(), "downloads_dir") {
		t.Fatalf("lexically escaped downloads_dir error = %v", err)
	}

	link := filepath.Join(tempRoot, "escape-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	throughLink := filepath.Join(link, "not-created-yet")
	if _, err := (&DownloadOverrides{TempDir: &throughLink}).ApplyValidated(base); err == nil || !strings.Contains(err.Error(), "temp_dir") {
		t.Fatalf("symlink-escaped temp_dir error = %v", err)
	}

	// Selecting the configured root itself remains valid.
	if _, err := (&DownloadOverrides{DownloadsDir: &downloadsRoot, TempDir: &tempRoot}).ApplyValidated(base); err != nil {
		t.Fatalf("configured roots rejected: %v", err)
	}
}

func TestOverridesEmptyListSurvivesJSONRoundTrip(t *testing.T) {
	extras := []string{}
	overrides := &DownloadOverrides{LyricsExtras: &extras}
	raw, err := json.Marshal(overrides)
	if err != nil {
		t.Fatal(err)
	}
	decoded := &DownloadOverrides{}
	if err := json.Unmarshal(raw, decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.LyricsExtras == nil || len(*decoded.LyricsExtras) != 0 {
		t.Fatalf("empty lyrics_extras override lost in round trip: %s -> %+v", raw, decoded.LyricsExtras)
	}

	// An explicitly empty override must clear the base list, while an absent
	// one keeps it.
	base := Default()
	base.Download.LyricsExtras = []string{"translation"}
	if got := decoded.Apply(base); len(got.Download.LyricsExtras) != 0 {
		t.Fatalf("empty override did not clear lyrics_extras: %v", got.Download.LyricsExtras)
	}
	if got := (&DownloadOverrides{}).Apply(base); len(got.Download.LyricsExtras) != 1 {
		t.Fatalf("absent override changed lyrics_extras: %v", got.Download.LyricsExtras)
	}
}

func TestRuntimeLockedChanges(t *testing.T) {
	base := Default()

	if got := RuntimeLockedChanges(base, base); len(got) != 0 {
		t.Fatalf("no-op change reported locked fields: %v", got)
	}

	updated := base
	updated.Download.QualityPriority = []string{"aac"}
	updated.Download.EmbedLyrics = false
	updated.Simulate.Enabled = true
	updated.Simulate.MinSpeedKBps = 10
	updated.Catalog.AlbumTrackURLMode = "album"
	updated.Catalog.SignedModeHLSSource = "web_token"
	updated.Logging.Level = "debug"
	updated.Logging.AccessLog = false
	if got := RuntimeLockedChanges(base, updated); len(got) != 0 {
		t.Fatalf("runtime-updatable changes reported as locked: %v", got)
	}

	updated = base
	updated.Server.Listen = "0.0.0.0:9999"
	updated.Logging.Format = "json"
	updated.Download.MaxRunningJobs = base.Download.MaxRunningJobs + 1
	updated.Download.MaxParallelDownloads = base.Download.MaxParallelDownloads + 1
	updated.Download.MaxParallelDecrypts = base.Download.MaxParallelDecrypts + 1
	updated.Download.MaxParallelWrapperRequests = base.Download.MaxParallelWrapperRequests + 1
	updated.Wrapper.Address = "10.0.0.1:8080"
	updated.Catalog.AllowedOrigins = []string{"https://example.com"}
	updated.Catalog.MaxParallelRequests = base.Catalog.MaxParallelRequests + 1
	updated.Catalog.RequestsPerSecond = base.Catalog.RequestsPerSecond + 1
	updated.Catalog.RequestBurst = base.Catalog.RequestBurst + 1
	got := RuntimeLockedChanges(base, updated)
	// Keys surface in Config struct-field order because the locked set is
	// derived from envFields.
	want := []string{"server.listen", "logging.format", "wrapper.address", "catalog.max_parallel_requests", "catalog.requests_per_second", "catalog.request_burst", "catalog.allowed_origins", "download.max_running_jobs", "download.max_parallel_downloads", "download.max_parallel_decrypts", "download.max_parallel_wrapper_requests"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("locked changes = %v, want %v", got, want)
	}
}

func TestMutableViewOmitsStartupBoundFields(t *testing.T) {
	view := MutableView(Default())
	if len(view) != 4 {
		t.Fatalf("view sections = %v, want catalog/download/logging/simulate only", view)
	}
	download, ok := view["download"].(map[string]any)
	if !ok {
		t.Fatalf("download section = %T, want map", view["download"])
	}
	for _, key := range []string{"max_running_jobs", "max_parallel_downloads", "max_parallel_decrypts"} {
		if _, exists := download[key]; exists {
			t.Fatalf("download section must not expose %s", key)
		}
	}
	if download["cover_format"] != "jpg" {
		t.Fatalf("download.cover_format = %v, want jpg", download["cover_format"])
	}
	if download["memory_mode"] != MemoryModeLow {
		t.Fatalf("download.memory_mode = %v, want low", download["memory_mode"])
	}
	for _, key := range []string{"max_parallel_downloads", "max_parallel_decrypts", "max_parallel_wrapper_requests"} {
		if _, ok := download[key]; ok {
			t.Fatalf("download section exposes startup-bound %s: %v", key, download)
		}
	}
	catalog, ok := view["catalog"].(map[string]any)
	if !ok || len(catalog) != 3 || catalog["album_track_url_mode"] != "song" || catalog["media_user_token"] != "" || catalog["signed_mode_hls_source"] != "wrapper" {
		t.Fatalf("catalog section = %v, want album_track_url_mode/media_user_token/signed_mode_hls_source", view["catalog"])
	}
	logging, ok := view["logging"].(map[string]any)
	if !ok || len(logging) != 2 || logging["level"] != "info" || logging["access_log"] != false {
		t.Fatalf("logging section = %v, want only level/access_log", view["logging"])
	}
}

func TestStoreGetSet(t *testing.T) {
	store := NewStore(Default())
	if got := store.Get(); got.Download.CoverFormat != "jpg" {
		t.Fatalf("initial snapshot = %+v", got.Download)
	}
	updated := Default()
	updated.Download.CoverFormat = "png"
	store.Set(updated)
	if got := store.Get(); got.Download.CoverFormat != "png" {
		t.Fatalf("snapshot after Set = %+v", got.Download)
	}
}

// TestApplyValidatedClampsNumericOverrides covers persisted jobs that predate
// the hard limits: their stored numeric overrides re-run through validation
// on every retry and post-restart requeue, so over-limit values must clamp
// instead of failing the job forever.
func TestApplyValidatedClampsNumericOverrides(t *testing.T) {
	attempts := 50
	applied, err := (&DownloadOverrides{MaxAttempts: &attempts}).ApplyValidated(Default())
	if err != nil {
		t.Fatalf("ApplyValidated() with over-limit numeric overrides failed: %v", err)
	}
	if applied.Download.MaxAttempts != maxAttemptsLimit {
		t.Fatalf("override not clamped: attempts=%d", applied.Download.MaxAttempts)
	}
}

// TestApplyValidatedStrictRejectsOverLimitOverrides covers fresh client
// submissions: unlike persisted legacy rows, over-limit numeric overrides
// must be rejected so job submission matches the runtime config API contract.
func TestApplyValidatedStrictRejectsOverLimitOverrides(t *testing.T) {
	attempts := 50
	if _, err := (&DownloadOverrides{MaxAttempts: &attempts}).ApplyValidatedStrict(Default()); err == nil || !strings.Contains(err.Error(), "max_attempts") {
		t.Fatalf("ApplyValidatedStrict() error = %v, want max_attempts bounds error", err)
	}
}
