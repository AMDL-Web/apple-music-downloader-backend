package config

import (
	"encoding/json"
	"reflect"
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
	parallel := 7
	quality := []string{"aac"}
	overrides := &DownloadOverrides{
		QualityPriority:   &quality,
		EmbedCover:        &embed,
		CoverFormat:       &format,
		MaxParallelTracks: &parallel,
	}
	got := overrides.Apply(base)

	if !reflect.DeepEqual(got.Download.QualityPriority, []string{"aac"}) {
		t.Fatalf("quality_priority = %v, want [aac]", got.Download.QualityPriority)
	}
	if got.Download.EmbedCover != false || got.Download.CoverFormat != "png" || got.Download.MaxParallelTracks != 7 {
		t.Fatalf("overridden fields not applied: %+v", got.Download)
	}
	// Untouched fields keep the base values, including false-able booleans.
	if got.Download.EmbedLyrics != base.Download.EmbedLyrics || got.Download.SongPathFormat != base.Download.SongPathFormat {
		t.Fatalf("unset fields changed: %+v", got.Download)
	}
	// The base config must not be mutated in place.
	if base.Download.EmbedCover != true || base.Download.CoverFormat != "jpg" {
		t.Fatalf("Apply mutated the base config: %+v", base.Download)
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
	updated.Wrapper.Address = "10.0.0.1:8080"
	updated.Catalog.AllowedOrigins = []string{"https://example.com"}
	got := RuntimeLockedChanges(base, updated)
	want := []string{"server.listen", "logging.format", "wrapper.address", "catalog.allowed_origins", "download.max_running_jobs"}
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
	if _, exists := download["max_running_jobs"]; exists {
		t.Fatal("download section must not expose max_running_jobs")
	}
	if download["cover_format"] != "jpg" {
		t.Fatalf("download.cover_format = %v, want jpg", download["cover_format"])
	}
	catalog, ok := view["catalog"].(map[string]any)
	if !ok || len(catalog) != 4 || catalog["album_track_url_mode"] != "song" || catalog["media_user_token"] != "" || catalog["media_user_token_priority"] != "config" || catalog["signed_mode_hls_source"] != "wrapper" {
		t.Fatalf("catalog section = %v, want album_track_url_mode/media_user_token/media_user_token_priority/signed_mode_hls_source", view["catalog"])
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
