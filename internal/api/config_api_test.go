package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"amdl/internal/config"
	"amdl/internal/db"
	"amdl/internal/domain"
	"amdl/internal/events"
	"amdl/internal/jobs"
	"amdl/internal/logging"
)

func TestGetConfigReturnsOnlyMutableFields(t *testing.T) {
	server := &Server{cfg: config.NewStore(config.Default())}
	recorder := requestJSON(t, server.Routes(), http.MethodGet, "/api/v1/config", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var resp struct {
		Config    map[string]json.RawMessage `json:"config"`
		Persisted bool                       `json:"persisted"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Persisted {
		t.Fatal("persisted = true, runtime config is never written back to disk")
	}
	for _, section := range []string{"catalog", "download", "logging", "simulate"} {
		if _, ok := resp.Config[section]; !ok {
			t.Fatalf("config missing mutable section %q: %s", section, recorder.Body.String())
		}
	}
	for _, section := range []string{"server", "database", "wrapper", "tools"} {
		if _, ok := resp.Config[section]; ok {
			t.Fatalf("config exposes startup-bound section %q: %s", section, recorder.Body.String())
		}
	}
	var download map[string]any
	if err := json.Unmarshal(resp.Config["download"], &download); err != nil {
		t.Fatal(err)
	}
	if _, ok := download["max_running_jobs"]; ok {
		t.Fatal("download section exposes startup-bound max_running_jobs")
	}
	if download["cover_format"] != "jpg" {
		t.Fatalf("download.cover_format = %v, want jpg", download["cover_format"])
	}
	var catalog map[string]any
	if err := json.Unmarshal(resp.Config["catalog"], &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 1 || catalog["album_track_url_mode"] != "song" {
		t.Fatalf("catalog section = %v, want only album_track_url_mode", catalog)
	}
	var logging map[string]any
	if err := json.Unmarshal(resp.Config["logging"], &logging); err != nil {
		t.Fatal(err)
	}
	if len(logging) != 2 || logging["level"] != "info" || logging["access_log"] != false {
		t.Fatalf("logging section = %v, want only level/access_log", logging)
	}
}

func TestUpdateConfigMergesAndTakesEffect(t *testing.T) {
	store := config.NewStore(config.Default())
	server := &Server{cfg: store}

	recorder := requestJSON(t, server.Routes(), http.MethodPut, "/api/v1/config",
		`{"download":{"embed_lyrics":false,"cover_format":"png"},"simulate":{"enabled":true,"min_speed_kbps":10,"max_speed_kbps":20}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	// The response echoes the mutable view only, never startup-bound sections.
	if strings.Contains(recorder.Body.String(), `"server"`) || strings.Contains(recorder.Body.String(), `"max_running_jobs"`) {
		t.Fatalf("update response leaks startup-bound fields: %s", recorder.Body.String())
	}

	got := store.Get()
	if got.Download.EmbedLyrics || got.Download.CoverFormat != "png" || !got.Simulate.Enabled {
		t.Fatalf("update not applied to store: %+v %+v", got.Download, got.Simulate)
	}
	// Merge semantics: fields absent from the body keep their current values.
	base := config.Default()
	if got.Download.SongPathFormat != base.Download.SongPathFormat || got.Server.Listen != base.Server.Listen {
		t.Fatalf("omitted fields changed: %+v", got)
	}
}

func TestUpdateConfigAppliesLoggingLevel(t *testing.T) {
	cfg := config.Default()
	cfg.Logging.Console = false
	system, err := logging.New(cfg.Logging)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = system.Close() })
	store := config.NewStore(cfg)
	server := NewServer(store, nil, nil, nil, nil, nil, nil, system.Logger, system)

	system.Logger.Debug("before-level-change")
	recorder := requestJSON(t, server.Routes(), http.MethodPut, "/api/v1/config", `{"logging":{"level":"debug","access_log":false}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	system.Logger.Debug("after-level-change")
	page := system.Store.List(logging.Filter{Query: "level-change", Limit: 10})
	if len(page.Entries) != 1 || page.Entries[0].Message != "after-level-change" {
		t.Fatalf("runtime level entries = %#v", page.Entries)
	}
	if got := store.Get().Logging; got.Level != "debug" || got.AccessLog {
		t.Fatalf("runtime logging config = %+v", got)
	}
}

func TestGetConfigReloadsManualFileEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	base := config.Default()
	if err := config.Save(path, base); err != nil {
		t.Fatal(err)
	}
	server := &Server{cfg: config.NewFileStore(base, path)}

	// Edit the file behind the running store, as a user would with an editor.
	edited := base
	edited.Download.CoverFormat = "png"
	if err := config.Save(path, edited); err != nil {
		t.Fatal(err)
	}
	recorder := requestJSON(t, server.Routes(), http.MethodGet, "/api/v1/config", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"cover_format":"png"`) {
		t.Fatalf("GET did not pick up the manual file edit: %s", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "reload_error") {
		t.Fatalf("unexpected reload_error: %s", recorder.Body.String())
	}
	if got := server.cfg.Get(); got.Download.CoverFormat != "png" {
		t.Fatalf("store snapshot not refreshed: %+v", got.Download)
	}

	// A broken file (edit in progress) must not break GET: the last good
	// snapshot is served and reload_error reports the problem.
	if err := os.WriteFile(path, []byte("download: ["), 0o644); err != nil {
		t.Fatal(err)
	}
	recorder = requestJSON(t, server.Routes(), http.MethodGet, "/api/v1/config", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "reload_error") {
		t.Fatalf("missing reload_error for broken file: %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"cover_format":"png"`) {
		t.Fatalf("broken file must keep serving the last good snapshot: %s", recorder.Body.String())
	}
}

func TestUpdateConfigPersistsToBackingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	server := &Server{cfg: config.NewFileStore(config.Default(), path)}

	recorder := requestJSON(t, server.Routes(), http.MethodPut, "/api/v1/config", `{"download":{"cover_format":"png"}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"persisted":true`) {
		t.Fatalf("response does not report persisted=true: %s", recorder.Body.String())
	}
	// The change must survive a restart: reloading the file yields it back.
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload persisted config: %v", err)
	}
	if loaded.Download.CoverFormat != "png" {
		t.Fatalf("persisted cover_format = %q, want png", loaded.Download.CoverFormat)
	}
}

func TestUpdateConfigRejectsBadInput(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		status int
		want   string
	}{
		{name: "unknown field", body: `{"download":{"nope":true}}`, status: http.StatusBadRequest, want: "unknown field"},
		{name: "malformed json", body: `{`, status: http.StatusBadRequest, want: ""},
		{name: "validation failure", body: `{"download":{"cover_format":"gif"}}`, status: http.StatusUnprocessableEntity, want: "cover_format"},
		{name: "locked field", body: `{"server":{"listen":"0.0.0.0:1"}}`, status: http.StatusUnprocessableEntity, want: "server.listen"},
		{name: "locked worker count", body: `{"download":{"max_running_jobs":99}}`, status: http.StatusUnprocessableEntity, want: "download.max_running_jobs"},
		{name: "locked logging output", body: `{"logging":{"format":"json"}}`, status: http.StatusUnprocessableEntity, want: "logging.format"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := config.NewStore(config.Default())
			server := &Server{cfg: store}
			recorder := requestJSON(t, server.Routes(), http.MethodPut, "/api/v1/config", tc.body)
			if recorder.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", recorder.Code, tc.status, recorder.Body.String())
			}
			if tc.want != "" && !strings.Contains(recorder.Body.String(), tc.want) {
				t.Fatalf("error body %q does not mention %q", recorder.Body.String(), tc.want)
			}
			if got := store.Get(); got.Download.CoverFormat != "jpg" || got.Server.Listen != config.Default().Server.Listen {
				t.Fatalf("rejected update leaked into the store: %+v", got)
			}
		})
	}
}

func TestUpdateConfigRejectsEnvPinnedFields(t *testing.T) {
	t.Setenv("AMDL_DOWNLOAD_COVER_FORMAT", "jpg")
	store := config.NewStore(config.Default())
	server := &Server{cfg: store}
	recorder := requestJSON(t, server.Routes(), http.MethodPut, "/api/v1/config", `{"download":{"cover_format":"png"}}`)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); !strings.Contains(body, "AMDL_DOWNLOAD_COVER_FORMAT") || !strings.Contains(body, "pinned by environment") {
		t.Fatalf("error body %q must name the pinning variable", body)
	}
	if store.Get().Download.CoverFormat != "jpg" {
		t.Fatalf("rejected update leaked into the store: %+v", store.Get().Download)
	}
	// Leaving a pinned field at its current value is still accepted.
	recorder = requestJSON(t, server.Routes(), http.MethodPut, "/api/v1/config", `{"download":{"cover_format":"jpg","embed_cover":false}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	if got := store.Get().Download; got.EmbedCover || got.CoverFormat != "jpg" {
		t.Fatalf("update alongside unchanged pinned field not applied: %+v", got)
	}
}

func TestCreateDownloadWithOverrides(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	manager := jobs.NewManager(store, events.NewHub(), stubProcessor{}, 1, slog.Default())
	server := &Server{cfg: config.NewStore(config.Default()), store: store, manager: manager}

	recorder := requestJSON(t, server.Routes(), http.MethodPost, "/api/v1/downloads",
		`{"urls":["song|us|1"],"overrides":{"embed_lyrics":false,"quality_priority":["aac"]}}`)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var resp domain.BatchSubmitResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Accepted != 1 || resp.Results[0].Job == nil {
		t.Fatalf("submit response = %+v", resp)
	}
	if o := resp.Results[0].Job.Overrides; o == nil || o.EmbedLyrics == nil || *o.EmbedLyrics {
		t.Fatalf("accepted job overrides = %+v, want embed_lyrics=false", resp.Results[0].Job.Overrides)
	}

	// The overrides must survive the DB round-trip so retries and
	// post-restart requeues run under the same overlay.
	persisted, err := store.GetJob(t.Context(), resp.Results[0].Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Overrides == nil || persisted.Overrides.EmbedLyrics == nil || *persisted.Overrides.EmbedLyrics {
		t.Fatalf("persisted overrides = %+v, want embed_lyrics=false", persisted.Overrides)
	}
	if qp := persisted.Overrides.QualityPriority; qp == nil || len(*qp) != 1 || (*qp)[0] != "aac" {
		t.Fatalf("persisted quality_priority override = %v, want [aac]", qp)
	}
}

func TestCreateDownloadRejectsUnknownFields(t *testing.T) {
	server := &Server{cfg: config.NewStore(config.Default())}
	recorder := requestJSON(t, server.Routes(), http.MethodPost, "/api/v1/downloads",
		`{"urls":["song|us|1"],"overrides":{"embedd_lyrics":false}}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s (a typo inside overrides must not be silently ignored)", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "embedd_lyrics") {
		t.Fatalf("error body %q does not name the unknown field", recorder.Body.String())
	}
}

func TestCreateDownloadRejectsInvalidOverrides(t *testing.T) {
	server := &Server{cfg: config.NewStore(config.Default())}
	recorder := requestJSON(t, server.Routes(), http.MethodPost, "/api/v1/downloads",
		`{"urls":["song|us|1"],"overrides":{"lyrics_format":"srt"}}`)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "invalid overrides") {
		t.Fatalf("error body %q does not mention invalid overrides", recorder.Body.String())
	}
}
