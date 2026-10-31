package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"amdl/internal/hookpreset"
	"amdl/internal/hooks"
	"amdl/internal/jobs"
)

const hooksFile = `enabled: false
entries:
  - name: cleanup
    type: exec
    events: [job_failed]
    command: "secret-command --token abc"
`

// hooksServer is a Server whose hook routes write a hooks.yaml in a temporary
// directory, and the file's path.
func hooksServer(t *testing.T) (http.Handler, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hooks.yaml")
	if err := os.WriteFile(path, []byte(hooksFile), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := hooks.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	d := hooks.NewDispatcher(cfg, nil, slog.Default())
	manager := jobs.NewManager(nil, nil, nil, 1, slog.Default())
	manager.SetHooks(d)
	return (&Server{manager: manager, hooks: hooks.NewEditor(path, d)}).Routes(), path
}

func TestHookWritesChangeOnlyWebhooks(t *testing.T) {
	routes, path := hooksServer(t)

	recorder := requestJSON(t, routes, http.MethodPut, "/api/v1/hooks/notify",
		`{"events":["job_finished"],"url":"https://ntfy.sh/amdl-topic","headers":{"Title":"AMDL"},"body":"{{.job.title}}"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("create: status %d, body %s", recorder.Code, recorder.Body.String())
	}
	var entry hooks.ListedEntry
	if err := json.Unmarshal(recorder.Body.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Origin != "https://ntfy.sh" || entry.Method != "POST" || !entry.Enabled || strings.Contains(recorder.Body.String(), "amdl-topic") {
		t.Errorf("created entry %s", recorder.Body.String())
	}

	// An omitted field keeps its value: switching the hook off keeps the
	// address, its headers and its body.
	if recorder := requestJSON(t, routes, http.MethodPut, "/api/v1/hooks/notify", `{"enabled":false}`); recorder.Code != http.StatusOK {
		t.Fatalf("patch: status %d, body %s", recorder.Code, recorder.Body.String())
	}
	cfg, err := hooks.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := cfg.Entries[1]; n.IsEnabled() || n.URL != "https://ntfy.sh/amdl-topic" || n.Headers["Title"] != "AMDL" || n.Body != "{{.job.title}}" {
		t.Errorf("notify after the patch: %+v", n)
	}
	if cfg.Entries[0].Command != "secret-command --token abc" {
		t.Errorf("the command hook changed: %+v", cfg.Entries[0])
	}

	recorder = requestJSON(t, routes, http.MethodPut, "/api/v1/hooks", `{"enabled":true}`)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"enabled":true`) {
		t.Fatalf("switch: status %d, body %s", recorder.Code, recorder.Body.String())
	}
	listing := requestJSON(t, routes, http.MethodGet, "/api/v1/hooks", "").Body.String()
	if !strings.Contains(listing, `"enabled":true,"entries"`) || !strings.Contains(listing, `"origin":"https://ntfy.sh"`) {
		t.Errorf("GET /hooks after the writes: %s", listing)
	}

	if recorder := requestJSON(t, routes, http.MethodDelete, "/api/v1/hooks/notify", ""); recorder.Code != http.StatusOK {
		t.Fatalf("delete: status %d, body %s", recorder.Code, recorder.Body.String())
	}
	assertErrorCode(t, requestJSON(t, routes, http.MethodDelete, "/api/v1/hooks/notify", ""), http.StatusNotFound, CodeHookNotFound)
}

func TestHookWritesRefuseCommandsAndBadEntries(t *testing.T) {
	routes, path := hooksServer(t)
	for _, c := range []struct {
		method, path, body string
		status             int
		code               ErrorCode
	}{
		{http.MethodPut, "/api/v1/hooks/cleanup", `{"enabled":true}`, http.StatusConflict, CodeHookReadOnly},
		{http.MethodDelete, "/api/v1/hooks/cleanup", "", http.StatusConflict, CodeHookReadOnly},
		{http.MethodPost, "/api/v1/hooks/cleanup/test", "", http.StatusConflict, CodeHookReadOnly},
		{http.MethodPut, "/api/v1/hooks/run", `{"type":"exec","events":["job_finished"]}`, http.StatusBadRequest, CodeInvalidRequest},
		{http.MethodPut, "/api/v1/hooks/run", `{"command":"rm -rf /","events":["job_finished"]}`, http.StatusBadRequest, CodeInvalidRequest},
		{http.MethodPut, "/api/v1/hooks/notify", `{"events":["job_finished"],"url":"file:///etc/passwd"}`, http.StatusUnprocessableEntity, CodeInvalidHook},
		{http.MethodPut, "/api/v1/hooks/notify", `{"url":"https://example.com"}`, http.StatusUnprocessableEntity, CodeInvalidHook},
		{http.MethodPut, "/api/v1/hooks", `{}`, http.StatusBadRequest, CodeInvalidRequest},
		{http.MethodPost, "/api/v1/hooks/nobody/test", "", http.StatusNotFound, CodeHookNotFound},
	} {
		recorder := requestJSON(t, routes, c.method, c.path, c.body)
		t.Run(c.method+" "+c.path+" "+c.body, func(t *testing.T) { assertErrorCode(t, recorder, c.status, c.code) })
	}
	if raw, _ := os.ReadFile(path); string(raw) != hooksFile {
		t.Errorf("a refused write changed hooks.yaml:\n%s", raw)
	}

	// A process without a hooks.yaml path cannot write one.
	assertErrorCode(t, requestJSON(t, (&Server{}).Routes(), http.MethodPut, "/api/v1/hooks", `{"enabled":true}`),
		http.StatusServiceUnavailable, CodeFeatureUnavailable)
}

func TestHookTestReportsTheEndpointsAnswer(t *testing.T) {
	routes, _ := hooksServer(t)
	status := http.StatusNoContent
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
	defer endpoint.Close()
	if recorder := requestJSON(t, routes, http.MethodPut, "/api/v1/hooks/plex",
		`{"events":["job_finished"],"method":"GET","url":"`+endpoint.URL+`/library/sections/all/refresh"}`); recorder.Code != http.StatusOK {
		t.Fatalf("create: status %d, body %s", recorder.Code, recorder.Body.String())
	}
	if got := requestJSON(t, routes, http.MethodPost, "/api/v1/hooks/plex/test", ""); got.Code != http.StatusOK || strings.TrimSpace(got.Body.String()) != `{"succeeded":true}` {
		t.Errorf("test against a 204: %d %s", got.Code, got.Body.String())
	}
	status = http.StatusUnauthorized
	got := requestJSON(t, routes, http.MethodPost, "/api/v1/hooks/plex/test", "")
	var result struct {
		Succeeded bool   `json:"succeeded"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &result); err != nil || got.Code != http.StatusOK || result.Succeeded || result.Error != "unexpected status 401" {
		t.Errorf("test against a 401: %d %s", got.Code, got.Body.String())
	}
}

func TestAPresetBuildsTheWebhook(t *testing.T) {
	routes, path := hooksServer(t)
	var presets struct {
		Presets []hookpreset.Preset `json:"presets"`
	}
	if err := json.Unmarshal(requestJSON(t, routes, http.MethodGet, "/api/v1/hooks/presets", "").Body.Bytes(), &presets); err != nil || len(presets.Presets) != len(hookpreset.All) || presets.Presets[0].ID != "ntfy" {
		t.Fatalf("GET /hooks/presets: %+v, %v", presets, err)
	}

	got := make(chan map[string]any, 1)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body) // a bad body fails the check below
		got <- body
	}))
	defer endpoint.Close()
	recorder := requestJSON(t, routes, http.MethodPut, "/api/v1/hooks/phone",
		`{"preset":"bark","values":{"server":"`+endpoint.URL+`","key":"device-1"}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("create from a preset: %d %s", recorder.Code, recorder.Body.String())
	}
	var entry hooks.ListedEntry
	_ = json.Unmarshal(recorder.Body.Bytes(), &entry) // checked field by field next
	if entry.Preset != "bark" || len(entry.Events) != 2 || strings.Contains(recorder.Body.String(), "device-1") {
		t.Errorf("entry from the Bark preset: %s", recorder.Body.String())
	}
	// The body the preset wrote renders, sent, as Bark's push asks.
	if r := requestJSON(t, routes, http.MethodPost, "/api/v1/hooks/phone/test", ""); !strings.Contains(r.Body.String(), `"succeeded":true`) {
		t.Fatalf("test: %s", r.Body.String())
	}
	if body := <-got; body["device_key"] != "device-1" || body["title"] != "AMDL" || body["body"] != "Downloaded Test from amdl — amdl · 1 tracks" {
		t.Errorf("Bark got %v", body)
	}

	// A request written by hand is no preset's.
	if r := requestJSON(t, routes, http.MethodPut, "/api/v1/hooks/phone", `{"url":"`+endpoint.URL+`/other"}`); r.Code != http.StatusOK || strings.Contains(r.Body.String(), `"preset"`) {
		t.Errorf("a raw write kept the preset: %s", r.Body.String())
	}
	cfg, _ := hooks.LoadConfig(path)
	if cfg.Entries[1].Preset != "" || cfg.Entries[1].Body == "" {
		t.Errorf("phone after the raw write: %+v", cfg.Entries[1])
	}

	for _, c := range []struct {
		body   string
		status int
		code   ErrorCode
	}{
		{`{"preset":"ntfy","values":{"topic":"t"},"url":"https://x.example"}`, http.StatusBadRequest, CodeInvalidRequest},
		{`{"preset":"pager"}`, http.StatusBadRequest, CodeInvalidRequest},
		{`{"values":{"topic":"t"}}`, http.StatusBadRequest, CodeInvalidRequest},
		{`{"preset":"ntfy","values":{}}`, http.StatusUnprocessableEntity, CodeInvalidHook},
		{`{"preset":"ntfy","values":{"topic":"t","command":"x"}}`, http.StatusUnprocessableEntity, CodeInvalidHook},
	} {
		assertErrorCode(t, requestJSON(t, routes, http.MethodPut, "/api/v1/hooks/new", c.body), c.status, c.code)
	}
}
