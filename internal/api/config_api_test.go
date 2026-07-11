package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"amdl/internal/config"
	"amdl/internal/db"
	"amdl/internal/domain"
	"amdl/internal/events"
	"amdl/internal/jobs"
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
	for _, section := range []string{"catalog", "download", "simulate"} {
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
	if len(persisted.Overrides.QualityPriority) != 1 || persisted.Overrides.QualityPriority[0] != "aac" {
		t.Fatalf("persisted quality_priority override = %v, want [aac]", persisted.Overrides.QualityPriority)
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
