package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"amdl/internal/db"
	"amdl/internal/hooks"
	"amdl/internal/jobs"
	"amdl/internal/media"
)

func TestHealthEndpoint(t *testing.T) {
	store, err := db.Open(t.TempDir() + "/health.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	fake := &fakeWrapperService{statusErr: errors.New("should not be called")}
	server := &Server{store: store, wrapper: fake}
	recorder := requestJSON(t, server.Routes(), http.MethodGet, "/api/v1/health", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if fake.statusCalls != 0 {
		t.Fatalf("health called wrapper.Status %d times", fake.statusCalls)
	}
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Fatalf("unexpected body: %#v", body)
	}
}

func TestHealthEndpointWithoutStore(t *testing.T) {
	server := &Server{}
	recorder := requestJSON(t, server.Routes(), http.MethodGet, "/api/v1/health", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestHealthEndpointDatabaseUnavailable(t *testing.T) {
	store, err := db.Open(t.TempDir() + "/health-closed.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	server := &Server{store: store}
	recorder := requestJSON(t, server.Routes(), http.MethodGet, "/api/v1/health", "")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d, body = %s", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
	}
}

func TestHooksEndpoint(t *testing.T) {
	t.Run("configured entries", func(t *testing.T) {
		disabled := false
		manager := jobs.NewManager(nil, nil, nil, 1, slog.Default())
		manager.SetHooks(hooks.NewDispatcher(hooks.Config{Enabled: true, Entries: []hooks.Entry{
			{
				Name: "notify", Type: "webhook", Events: []string{"job_finished"},
				URL: "https://secret.example/token", Headers: map[string]string{"Authorization": "Bearer secret"},
			},
			{
				Name: "cleanup", Enabled: &disabled, Type: "exec", Events: []string{"job_failed"}, JobTypes: []string{"album"},
				Command: "secret-command --token abc", Workdir: "/secret/workdir",
			},
		}}, nil, slog.Default()))

		recorder := requestJSON(t, (&Server{manager: manager}).Routes(), http.MethodGet, "/api/v1/hooks", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		var got hooks.Listing
		if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		want := hooks.Listing{Enabled: true, Entries: []hooks.ListedEntry{
			{Name: "notify", Enabled: true, Type: "webhook", Events: []string{"job_finished"}, JobTypes: []string{},
				Method: "POST", Origin: "https://secret.example", HeaderNames: []string{"Authorization"}, MaxAttempts: 1},
			{Name: "cleanup", Enabled: false, Type: "exec", Events: []string{"job_failed"}, JobTypes: []string{"album"}},
		}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("response = %+v, want %+v", got, want)
		}
		body := recorder.Body.String()
		for _, forbidden := range []string{
			`"url"`, `"headers"`, `"command"`, `"workdir"`,
			"https://secret.example/token", "Bearer secret", "secret-command --token abc", "/secret/workdir",
		} {
			if strings.Contains(body, forbidden) {
				t.Errorf("response exposes forbidden hook configuration %q: %s", forbidden, body)
			}
		}
	})

	t.Run("missing configuration", func(t *testing.T) {
		recorder := requestJSON(t, (&Server{}).Routes(), http.MethodGet, "/api/v1/hooks", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		if got := strings.TrimSpace(recorder.Body.String()); got != `{"enabled":false,"entries":[]}` {
			t.Fatalf("body = %s, want disabled hooks with an empty array", got)
		}
	})
}

func TestQualityEndpointReturnsTracksForSong(t *testing.T) {
	song := media.QualitySong{ID: "2", Name: "One Last Kiss", Artist: "Hikaru Utada", Album: "One Last Kiss", HasLyrics: true}
	fake := &fakeQualityService{result: media.QualityResult{
		Input: "https://music.apple.com/cn/album/example/1?i=2", Storefront: "cn", Type: "song", AdamID: "2",
		Tracks: []media.QualityTrack{{
			Song:      song,
			Qualities: []media.QualityOption{{ID: "alac", Label: "Lossless", Available: true, CodecID: "audio-alac-stereo-44100-16", BitDepth: 16, SampleRate: 44100}},
		}},
	}}
	server := &Server{quality: fake}
	recorder := requestJSON(t, server.Routes(), http.MethodPost, "/api/v1/quality", `{"url":" https://music.apple.com/cn/album/example/1?i=2 "}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if fake.req.URL != "https://music.apple.com/cn/album/example/1?i=2" {
		t.Fatalf("quality request URL = %q", fake.req.URL)
	}
	var body media.QualityResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.AdamID != "2" || len(body.Tracks) != 1 || len(body.Tracks[0].Qualities) != 1 || body.Tracks[0].Qualities[0].ID != "alac" {
		t.Fatalf("unexpected quality response: %#v", body)
	}
	// Raw-body check so a renamed/mistyped json tag can't slip through the
	// struct round-trip above.
	if !strings.Contains(recorder.Body.String(), `"has_lyrics":true`) {
		t.Fatalf("quality body missing \"has_lyrics\":true: %s", recorder.Body.String())
	}
	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &topLevel); err != nil {
		t.Fatal(err)
	}
	if _, ok := topLevel["tracks"]; !ok {
		t.Fatalf("song response missing tracks: %s", recorder.Body.String())
	}
	if _, ok := topLevel["song"]; ok {
		t.Fatalf("song response contains legacy top-level song: %s", recorder.Body.String())
	}
	if _, ok := topLevel["qualities"]; ok {
		t.Fatalf("song response contains legacy top-level qualities: %s", recorder.Body.String())
	}
	if len(topLevel) != 5 {
		t.Fatalf("song response top-level keys = %#v, want input/storefront/type/adam_id/tracks", topLevel)
	}
}

func TestQualityEndpointReturnsCollectionTracks(t *testing.T) {
	fake := &fakeQualityService{result: media.QualityResult{
		Input: "https://music.apple.com/cn/album/example/1", Storefront: "cn", Type: "album", AdamID: "1",
		Tracks: []media.QualityTrack{{
			Song:      media.QualitySong{ID: "2", Name: "One Last Kiss", Artist: "Hikaru Utada", Album: "One Last Kiss"},
			Qualities: []media.QualityOption{{ID: "alac", Label: "Lossless", Available: true}},
		}},
	}}
	server := &Server{quality: fake}
	recorder := requestJSON(t, server.Routes(), http.MethodPost, "/api/v1/quality", `{"url":"https://music.apple.com/cn/album/example/1"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var body media.QualityResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Type != "album" || body.AdamID != "1" || len(body.Tracks) != 1 || body.Tracks[0].Song.ID != "2" {
		t.Fatalf("unexpected collection quality response: %#v", body)
	}
	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &topLevel); err != nil {
		t.Fatal(err)
	}
	if _, ok := topLevel["song"]; ok {
		t.Fatalf("collection response contains a legacy top-level song: %s", recorder.Body.String())
	}
	if _, ok := topLevel["qualities"]; ok {
		t.Fatalf("collection response contains legacy top-level qualities: %s", recorder.Body.String())
	}
	if len(topLevel) != 5 {
		t.Fatalf("collection response top-level keys = %#v, want input/storefront/type/adam_id/tracks", topLevel)
	}
}

func TestQualityEndpointValidatesURL(t *testing.T) {
	server := &Server{quality: &fakeQualityService{}}
	recorder := requestJSON(t, server.Routes(), http.MethodPost, "/api/v1/quality", `{"url":" "}`)
	assertErrorCode(t, recorder, http.StatusBadRequest, "invalid_request")
}

func TestQualityEndpointErrors(t *testing.T) {
	body := `{"url":"https://music.apple.com/us/album/x/1"}`
	server := &Server{quality: &fakeQualityService{err: errors.New("catalog lookup failed")}}
	recorder := requestJSON(t, server.Routes(), http.MethodPost, "/api/v1/quality", body)
	assertErrorCode(t, recorder, http.StatusBadGateway, "upstream_error")

	server = &Server{quality: &fakeQualityService{err: fmt.Errorf("resolve album: %w", notFoundError{})}}
	recorder = requestJSON(t, server.Routes(), http.MethodPost, "/api/v1/quality", body)
	assertErrorCode(t, recorder, http.StatusNotFound, CodeCatalogNotFound)
}
