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

func TestGetConfigReturnsRuntimeSnapshot(t *testing.T) {
	server := &Server{cfg: config.NewStore(config.Default())}
	recorder := requestJSON(t, server.Routes(), http.MethodGet, "/api/v1/config", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var resp struct {
		Config    config.Config `json:"config"`
		Persisted bool          `json:"persisted"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Persisted {
		t.Fatal("persisted = true, runtime config is never written back to disk")
	}
	if resp.Config.Download.CoverFormat != "jpg" || len(resp.Config.Download.QualityPriority) == 0 {
		t.Fatalf("config snapshot incomplete: %+v", resp.Config.Download)
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
