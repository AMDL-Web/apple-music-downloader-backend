package config

import (
	"context"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// layered builds the production-shaped store around a config file body, an
// explicit environment, and an in-memory settings backend standing in for the
// database.
func layered(t *testing.T, fileBody string, environ []string) (*Store, string) {
	t.Helper()
	return layeredWithSettings(t, fileBody, environ, nil)
}

// layeredWithSettings seeds the database layer directly, which is the only way
// to place a value under a key a higher layer already pins — Update refuses to
// write one, precisely because it would not take effect.
func layeredWithSettings(t *testing.T, fileBody string, environ []string, stored map[string]string) (*Store, string) {
	t.Helper()
	path := writeConfig(t, fileBody)
	resolver, err := NewResolver(path, environ)
	if err != nil {
		t.Fatalf("NewResolver() error = %v", err)
	}
	settings := &memorySettings{values: stored}
	store, err := NewLayeredStore(context.Background(), resolver, settings)
	if err != nil {
		t.Fatalf("NewLayeredStore() error = %v", err)
	}
	return store, path
}

func mustUpdate(t *testing.T, store *Store, body string) Config {
	t.Helper()
	patch, err := ParsePatch([]byte(body))
	if err != nil {
		t.Fatalf("ParsePatch(%s) error = %v", body, err)
	}
	cfg, err := store.Update(context.Background(), patch)
	if err != nil {
		t.Fatalf("Update(%s) error = %v", body, err)
	}
	return cfg
}

func updateErr(t *testing.T, store *Store, body string) error {
	t.Helper()
	patch, err := ParsePatch([]byte(body))
	if err != nil {
		t.Fatalf("ParsePatch(%s) error = %v", body, err)
	}
	_, err = store.Update(context.Background(), patch)
	if err == nil {
		t.Fatalf("Update(%s) succeeded, want rejection", body)
	}
	return err
}

// TestLayerPrecedence is the whole contract in one table: for a key set in
// several layers, the highest one wins and is the one reported.
func TestLayerPrecedence(t *testing.T) {
	store, _ := layeredWithSettings(t,
		"download:\n  cover_format: png\n  lyrics_format: ttml\n",
		[]string{"AMDL_DOWNLOAD_COVER_FORMAT=jpeg"},
		map[string]string{"download.cover_format": `"jpg"`, "download.lyrics_format": `"lrc"`})
	mustUpdate(t, store, `{"download":{"memory_mode":"high"}}`)

	cfg, sources := store.Get(), store.Sources()
	for _, tc := range []struct {
		key    string
		got    string
		want   string
		source Source
	}{
		// Set in all four layers: the environment wins.
		{key: "download.cover_format", got: cfg.Download.CoverFormat, want: "jpeg", source: SourceEnv},
		// Set in file and database: the file wins.
		{key: "download.lyrics_format", got: cfg.Download.LyricsFormat, want: "ttml", source: SourceFile},
		// Set in the database only.
		{key: "download.memory_mode", got: cfg.Download.MemoryMode, want: MemoryModeHigh, source: SourceDatabase},
		// Set nowhere.
		{key: "download.lyrics_type", got: cfg.Download.LyricsType, want: Default().Download.LyricsType, source: SourceDefault},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.key, tc.got, tc.want)
		}
		if sources[tc.key] != tc.source {
			t.Errorf("%s source = %q, want %q", tc.key, sources[tc.key], tc.source)
		}
	}
}

// TestFilePresenceLocksEvenAtTheDefaultValue pins the rule that presence, not
// difference, is what locks a key: an operator who writes the default value
// out still means it to stay put.
func TestFilePresenceLocksEvenAtTheDefaultValue(t *testing.T) {
	store, _ := layered(t, "download:\n  cover_format: jpg\n", nil)
	if source := store.Sources()["download.cover_format"]; source != SourceFile {
		t.Fatalf("cover_format source = %q, want file", source)
	}
	if locked := LockedView(store.Sources()); !slices.Contains(locked, "download.cover_format") {
		t.Fatalf("locked = %v, want download.cover_format", locked)
	}
	err := updateErr(t, store, `{"download":{"cover_format":"png"}}`)
	var rejected *RejectedError
	if !errors.As(err, &rejected) || rejected.Reason != RejectLocked {
		t.Fatalf("Update() error = %v, want a locked rejection", err)
	}
}

func TestUpdateRejectsStartupBoundKeys(t *testing.T) {
	store, _ := layered(t, "", nil)
	err := updateErr(t, store, `{"download":{"max_running_jobs":9,"cover_format":"png"}}`)
	var rejected *RejectedError
	if !errors.As(err, &rejected) || rejected.Reason != RejectStartupBound {
		t.Fatalf("Update() error = %v, want a startup-bound rejection", err)
	}
	if !slices.Equal(rejected.Keys, []string{"download.max_running_jobs"}) {
		t.Fatalf("rejected keys = %v, want only download.max_running_jobs", rejected.Keys)
	}
	// All-or-nothing: the acceptable key in the same patch is not written.
	if got := store.Get().Download.CoverFormat; got != Default().Download.CoverFormat {
		t.Fatalf("cover_format = %q, want the rejected patch to have written nothing", got)
	}
}

// TestUpdateAcceptsUnchangedLockedKeys keeps a client that echoes the whole
// config back working: only a real change to a pinned key is refused.
func TestUpdateAcceptsUnchangedLockedKeys(t *testing.T) {
	store, _ := layered(t, "download:\n  cover_format: png\n", nil)
	cfg := mustUpdate(t, store, `{"download":{"cover_format":"png","embed_cover":false}}`)
	if cfg.Download.CoverFormat != "png" || cfg.Download.EmbedCover {
		t.Fatalf("update alongside an unchanged pinned key = %+v", cfg.Download)
	}
	// Resetting a shadowed stored row changes nothing effective, so it is not
	// a change to the pinned key either.
	if _, err := store.Update(context.Background(), Patch{"download.cover_format": nil}); err != nil {
		t.Fatalf("reset of a shadowed key = %v, want acceptance", err)
	}
	if got := store.Get().Download.CoverFormat; got != "png" {
		t.Fatalf("cover_format = %q, want the file value to stand", got)
	}
}

func TestUpdateRejectsInvalidMergedConfig(t *testing.T) {
	store, _ := layered(t, "", nil)
	err := updateErr(t, store, `{"simulate":{"enabled":true,"min_speed_kbps":50,"max_speed_kbps":10}}`)
	var rejected *RejectedError
	if !errors.As(err, &rejected) || rejected.Reason != RejectInvalid {
		t.Fatalf("Update() error = %v, want a validation rejection", err)
	}
	if !strings.Contains(err.Error(), "max_speed_kbps") {
		t.Fatalf("error %v must name the failing key", err)
	}
	if store.Get().Simulate.Enabled {
		t.Fatal("rejected update leaked into the snapshot")
	}
}

// TestUpdateWritesOnlyTheDatabase is the file-safety half of the contract.
func TestUpdateWritesOnlyTheDatabase(t *testing.T) {
	body := "download:\n  memory_mode: high\n"
	store, path := layered(t, body, nil)
	mustUpdate(t, store, `{"download":{"cover_format":"png"}}`)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != body {
		t.Fatalf("config file changed:\n%s", raw)
	}
}

// TestReloadPicksUpFileEditsAndKeepsStartupValues covers the GET path: a hand
// edit to a runtime key applies, while a startup-bound edit is only reported,
// because the components built from it cannot follow a live change.
func TestReloadPicksUpFileEditsAndKeepsStartupValues(t *testing.T) {
	store, path := layered(t, "", nil)
	mustUpdate(t, store, `{"download":{"cover_format":"png"}}`)

	if err := os.WriteFile(path, []byte("server:\n  listen: \"0.0.0.0:19999\"\ndownload:\n  cover_format: jpeg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Reload(context.Background()); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	if got := store.Get().Download.CoverFormat; got != "jpeg" {
		t.Fatalf("cover_format = %q, want the file edit to win over the stored value", got)
	}
	if got := store.Get().Server.Listen; got != Default().Server.Listen {
		t.Fatalf("listen = %q, want the startup value the process was built with", got)
	}

	// A broken edit leaves the last good snapshot in place.
	if err := os.WriteFile(path, []byte("download: ["), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Reload(context.Background()); err == nil {
		t.Fatal("Reload() of a broken file succeeded, want an error")
	}
	if got := store.Get().Download.CoverFormat; got != "jpeg" {
		t.Fatalf("cover_format = %q, want the last good snapshot", got)
	}
}

// TestReloadRestoresStoredValueWhenTheFileReleasesTheKey covers the unpinning
// path: dropping a key from the file hands it back to the database layer,
// where the value an operator set earlier is still waiting.
func TestReloadRestoresStoredValueWhenTheFileReleasesTheKey(t *testing.T) {
	store, path := layered(t, "", nil)
	mustUpdate(t, store, `{"download":{"cover_format":"png"}}`)
	if err := os.WriteFile(path, []byte("download:\n  cover_format: jpeg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Reload(context.Background()); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Reload(context.Background()); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	if got := store.Get().Download.CoverFormat; got != "png" {
		t.Fatalf("cover_format = %q, want the stored value back", got)
	}
	if source := store.Sources()["download.cover_format"]; source != SourceDatabase {
		t.Fatalf("cover_format source = %q, want db", source)
	}
}

// TestMissingConfigFileIsNormal: the file is an override layer, so an install
// without one runs on stored settings and defaults alone.
func TestMissingConfigFileIsNormal(t *testing.T) {
	resolver, err := NewResolver(writeConfig(t, "")+".absent", nil)
	if err != nil {
		t.Fatalf("NewResolver() with no file error = %v", err)
	}
	if resolver.FileFound() {
		t.Fatal("FileFound() = true for a missing file")
	}
	cfg, sources, err := resolver.Resolve(map[string]string{"download.cover_format": `"png"`})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if cfg.Download.CoverFormat != "png" {
		t.Fatalf("cover_format = %q, want the stored value", cfg.Download.CoverFormat)
	}
	if len(LockedView(sources)) != 0 {
		t.Fatalf("locked = %v, want nothing pinned without a file", LockedView(sources))
	}
}

func TestResolveRejectsUnusableStoredSettings(t *testing.T) {
	resolver, err := NewResolver(writeConfig(t, ""), nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, settings := range map[string]map[string]string{
		"unknown key":      {"download.nope": `"x"`},
		"startup-bound":    {"download.max_running_jobs": `9`},
		"wrong type":       {"download.max_attempts": `"three"`},
		"malformed json":   {"download.cover_format": `png`},
		"removed v1.2 key": {"catalog.media_user_token_priority": `"request"`},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := resolver.Resolve(settings); err == nil {
				t.Fatalf("Resolve(%v) succeeded, want an error naming the row", settings)
			}
		})
	}
}

func TestParsePatchNormalizesAndTypeChecks(t *testing.T) {
	patch, err := ParsePatch([]byte(`{"download":{"max_attempts":5,"lyrics_extras":[],"cover_format":null}}`))
	if err != nil {
		t.Fatalf("ParsePatch() error = %v", err)
	}
	if !slices.Equal(patch.Keys(), []string{"download.cover_format", "download.lyrics_extras", "download.max_attempts"}) {
		t.Fatalf("keys = %v", patch.Keys())
	}
	if patch["download.cover_format"] != nil {
		t.Fatalf("explicit null must decode to a reset, got %q", *patch["download.cover_format"])
	}
	if got := *patch["download.max_attempts"]; got != "5" {
		t.Fatalf("max_attempts encoded as %q, want 5", got)
	}
	// An empty list is stored as one, not as the null a nil slice would give.
	if got := *patch["download.lyrics_extras"]; got != "[]" {
		t.Fatalf("lyrics_extras encoded as %q, want []", got)
	}

	for name, body := range map[string]string{
		"unknown key":     `{"download":{"nope":1}}`,
		"unknown section": `{"nope":{"x":1}}`,
		"wrong type":      `{"download":{"max_attempts":"five"}}`,
		"section scalar":  `{"download":3}`,
		"not an object":   `[1]`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePatch([]byte(body)); err == nil {
				t.Fatalf("ParsePatch(%s) succeeded, want an error", body)
			}
		})
	}
}

// TestInMemoryStoreUpdatesWithoutPersistence keeps the test-only constructor
// honest: updates merge onto the config it was given and nothing is persisted.
func TestInMemoryStoreUpdatesWithoutPersistence(t *testing.T) {
	base := Default()
	base.Download.TempDir = "/custom/tmp"
	store := NewStore(base)
	if store.Persistent() {
		t.Fatal("Persistent() = true for an in-memory store")
	}
	cfg := mustUpdate(t, store, `{"download":{"cover_format":"png"}}`)
	if cfg.Download.CoverFormat != "png" || cfg.Download.TempDir != "/custom/tmp" {
		t.Fatalf("in-memory update = %+v, want the patch merged onto the base config", cfg.Download)
	}
	if err := store.Reload(context.Background()); err != nil {
		t.Fatalf("Reload() on an in-memory store = %v, want a no-op", err)
	}
	if got := store.Get().Download.CoverFormat; got != "png" {
		t.Fatalf("cover_format = %q after reload, want the update to stand", got)
	}
}

func TestSourcesViewCoversExactlyTheMutableView(t *testing.T) {
	store, _ := layered(t, "", nil)
	view, sources := MutableView(store.Get()), SourcesView(store.Sources())
	keys := map[string]bool{}
	for section, body := range view {
		leaves, ok := body.(map[string]any)
		if !ok {
			t.Fatalf("section %q = %T, want a map", section, body)
		}
		for leaf := range leaves {
			keys[section+"."+leaf] = true
		}
	}
	if len(keys) != len(sources) {
		t.Fatalf("sources covers %d keys, mutable view has %d", len(sources), len(keys))
	}
	for key := range keys {
		if _, ok := sources[key]; !ok {
			t.Fatalf("sources is missing %q", key)
		}
	}
}

func TestFieldValueJSONMatchesStoredEncoding(t *testing.T) {
	cfg := Default()
	cfg.Download.QualityPriority = []string{"alac", "aac"}
	got, err := fieldValueJSON(cfg, "download.quality_priority")
	if err != nil {
		t.Fatal(err)
	}
	patch, err := ParsePatch([]byte(`{"download":{"quality_priority":["alac","aac"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := *patch["download.quality_priority"]; got != want {
		t.Fatalf("effective encoding %q != requested encoding %q; a locked key would look changed", got, want)
	}
	if !reflect.DeepEqual(got, `["alac","aac"]`) {
		t.Fatalf("encoding = %s", got)
	}
}
